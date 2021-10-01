/*
 * Copyright 2018 Ji-Young Park(jiyoung.park.dev@gmail.com)
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package driver

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	iscsilib "github.com/kubernetes-csi/csi-lib-iscsi/iscsi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	mu "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"

	"github.com/jparklab/synology-csi/pkg/synology/api/iscsi"
)

const (
	probeDeviceInterval = 1 * time.Second
	probeDeviceTimeout  = 60 * time.Second
)

type nodeServer struct {
	driver *driver

	targetAPI iscsi.TargetAPI
	lunAPI    iscsi.LunAPI

	iscsiPortals []string
}

// NodePublishVolume mounts the volume to target path
func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	targetPath := req.GetTargetPath()
	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	// TODO: support chap
	// secrets := req.GetNodePublishSecrets()

	targetID, mappingIndex, err := parseVolumeID(volID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	target, err := ns.targetAPI.Get(targetID)
	if err != nil {
		msg := fmt.Sprintf(
			"Unable to find target of ID: %d", targetID)
		klog.V(1).Info(msg)
		return nil, status.Error(codes.NotFound, msg)
	}

	if klog.V(2).Enabled() {
		iscsilib.EnableDebugLogging(IscsiLogWriter{})
	}

	// TODO(ressu): this really should be in a separate function
	// Generate target Info fields
	ti := []iscsilib.TargetInfo{}
	for _, p := range ns.iscsiPortals {
		ti = append(ti, iscsilib.TargetInfo{Iqn: target.IQN, Portal: p})
	}

	c := iscsilib.Connector{
		Targets:       ti,
		TargetIqn:     target.IQN,
		TargetPortals: ns.iscsiPortals,
		// TODO(ressu): Forcing discovery seems odd, we should be good by now
		DoDiscovery: true,
		Lun:         int32(mappingIndex),
	}

	devicePath, err := iscsilib.Connect(c)
	if err != nil {
		msg := fmt.Sprintf("failed to connect to iSCSI target %s: %v", target.IQN, err)
		klog.Error(msg)
		return nil, status.Errorf(codes.Internal, msg)
	}

	klog.V(1).Infof("Checking mount point: %s", targetPath)

	// mount device to the target path
	mounter := &mu.SafeFormatAndMount{
		Interface: mu.New(""),
		Exec:      utilexec.New(),
	}

	notMnt, err := mounter.IsLikelyNotMountPoint(targetPath)
	if !notMnt {
		klog.V(1).Infof("%s is already mounted", targetPath)
		return &csi.NodePublishVolumeResponse{}, nil
	}

	if err != nil {
		if !os.IsNotExist(err) {
			return nil, status.Errorf(codes.Internal, "failed to check mount target: %v", err)
		}
		klog.V(1).Infof("Creating mount directory: %s", targetPath)
		err = os.Mkdir(targetPath, 0750)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create mount directory: %v", err)
		}
	}

	exists, err := mu.PathExists(devicePath)
	if !exists || err != nil {
		msg := fmt.Sprintf("Could not find ISCSI device: %s", devicePath)
		klog.V(1).Info(msg)
		return nil, status.Error(codes.Internal, msg)
	}

	options := []string{"rw"}
	mountFlags := req.GetVolumeCapability().GetMount().GetMountFlags()
	options = append(options, mountFlags...)

	klog.V(1).Infof(
		"Mounting %s to %s(fstype: %s, options: %v)",
		devicePath, targetPath, fsType, options)
	err = mounter.FormatAndMount(devicePath, targetPath, fsType, options)
	if err != nil {
		msg := fmt.Sprintf(
			"Failed to mount %s to %s(fstype: %s, options: %v): %v",
			devicePath, targetPath, fsType, options, err)
		klog.V(1).Info(msg)
		return nil, status.Error(codes.Internal, msg)
	}

	// TODO(jpark):
	// change owner of the root path:
	// https://github.com/kubernetes/kubernetes/pull/62486
	//	 https://github.com/kubernetes/kubernetes/pull/62486/files
	// https://github.com/kubernetes/kubernetes/issues/66323
	//	https://github.com/kubernetes/kubernetes/pull/67280/files

	klog.V(1).Infof(
		"Mounted %s to %s(fstype: %s, options: %v)",
		devicePath, targetPath, fsType, options)

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volID := req.GetVolumeId()
	targetPath := req.GetTargetPath()

	targetID, _, err := parseVolumeID(volID)
	if err != nil {
		klog.Errorf("failed to parse volume ID '%s': %v", volID, err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	target, err := ns.targetAPI.Get(targetID)
	if err != nil {
		msg := fmt.Sprintf(
			"Unable to find target of ID: %d", targetID)
		klog.V(1).Info(msg)
		return nil, status.Error(codes.NotFound, msg)
	}

	mounter := &mu.SafeFormatAndMount{
		Interface: mu.New(""),
		Exec:      utilexec.New(),
	}

	notMnt, err := mounter.IsLikelyNotMountPoint(targetPath)
	if notMnt {
		msg := fmt.Sprintf("Path %s not mounted", targetPath)
		klog.V(1).Info(msg)
		return nil, status.Errorf(codes.NotFound, msg)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to detect if volume is mounted: %v", err)
	}

	if err = mounter.Unmount(targetPath); err != nil {
		msg := fmt.Sprintf("Failed to unmount %s: %v", targetPath, err)
		klog.V(1).Info(msg)
		return nil, status.Errorf(codes.Internal, msg)
	}

	if err = os.Remove(targetPath); err != nil {
		klog.Warning("failed to remove directory %s: %v", targetPath)
	}

	// logout target
	// NOTE: we can safely log out because we do not share the target
	//	and we only support targets with a single lun
	if err = iscsilib.Disconnect(target.IQN, ns.iscsiPortals); err != nil {
		msg := fmt.Sprintf(
			"Failed to logout(iqn: %s): %v", target.IQN, err)
		klog.V(1).Info(msg)
		return nil, status.Errorf(codes.Internal, msg)
	}

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeStageVolume temporarily mounts the volume to a staging path
func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	// No staging is necessary since we do not share volumes
	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	// No staging is necessary since we do not share volumes
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	volID := req.GetVolumeId()
	if volID == "" {
		msg := fmt.Sprintf("volume id must be defined")
		klog.V(1).Info(msg)
		return nil, status.Error(codes.InvalidArgument, msg)
	}

	volumePath := req.GetVolumePath()
	if volumePath == "" {
		msg := fmt.Sprintf("volume path must be defined")
		klog.V(1).Info(msg)
		return nil, status.Error(codes.InvalidArgument, msg)
	}

	mounter := &mu.SafeFormatAndMount{
		Interface: mu.New(""),
		Exec:      utilexec.New(),
	}

	devicePath, _, err := mu.GetDeviceNameFromMount(mounter, volumePath)
	if err != nil {
		klog.Errorf("cannot detect device path for volume %s: %v", volumePath, err)
		return nil, status.Errorf(codes.Internal, "Cannot detect device path for volume %s: %v", volumePath, err)
	}

	if devicePath == "" {
		klog.Errorf("no volume found for path: %s", volumePath)
		return nil, status.Errorf(codes.NotFound, "no volume found: %s", volumePath)
	}

	// ex) /sys/block/sdX/device/rescan is rescan device path
	blockDeviceRescanPath := ""
	parts := strings.Split(devicePath, "/")
	if len(parts) == 3 && strings.HasPrefix(parts[1], "dev") {
		d := filepath.Join("/sys/block", parts[2], "device", "rescan")
		blockDeviceRescanPath, err = filepath.EvalSymlinks(d)
		if err != nil {
			klog.Errorf("failed to detect device rescan path with %s: %v", d, err)
			return nil, status.Error(codes.Internal, "failed to rescan device")
		}
	} else {
		msg := fmt.Sprintf("device path %s is invalid format", devicePath)
		return nil, status.Error(codes.Internal, msg)
	}
	// write data for triggering to rescan
	err = ioutil.WriteFile(blockDeviceRescanPath, []byte{'1'}, 0666)
	if err != nil {
		klog.Errorf("unable to trigger device rescan: %v", err)
		return nil, status.Error(codes.Internal, "failed to rescan device")
	}

	// resize file system
	r := mu.NewResizeFs(utilexec.New())
	if _, err := r.Resize(devicePath, volumePath); err != nil {
		// TODO: this error doesn't make sense, rephrase
		klog.Errorf("could not resize volume %s to %s: %v", devicePath, volumePath, err)
		return nil, status.Errorf(codes.Internal, "Could not resize volume %s to %s: %s", devicePath, volumePath, err.Error())
	}

	return &csi.NodeExpandVolumeResponse{}, nil
}

func (ns *nodeServer) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	capabilities := []csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
	}

	caps := make([]*csi.NodeServiceCapability, len(capabilities))
	for i, capability := range capabilities {
		caps[i] = &csi.NodeServiceCapability{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: capability,
				},
			},
		}
	}

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: caps,
	}, nil
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: ns.driver.nodeID,
	}, nil
}

// Stubs for unimplemented calls
func (ns *nodeServer) NodeGetVolumeStats(ctx context.Context, in *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
