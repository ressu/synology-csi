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
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	"github.com/jparklab/synology-csi/pkg/synology/api/iscsi"
	"github.com/jparklab/synology-csi/pkg/synology/api/storage"
)

const (
	defaultVolumeSize      = int64(1 * 1024 * 1024 * 1024)
	defaultLocation        = "/volume1"
	defaultVolumeTypeExt4  = iscsi.LunTypeThin
	defaultVolumeTypeBtrfs = iscsi.LunTypeBlun

	targetNamePrefix = "kube-csi"
	lunNamePrefix    = "kube-csi"

	iqnPrefix = "iqn.2000-01.com.synology:kube-csi"
)

type controllerServer struct {
	driver    *driver
	targetAPI iscsi.TargetAPI
	lunAPI    iscsi.LunAPI
	volumeAPI storage.VolumeAPI
}

func (cs *controllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	volID := req.GetVolumeId()

	targetID, mappingIndex, err := parseVolumeID(volID)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	target, err := cs.targetAPI.Get(targetID)
	if err != nil {
		msg := fmt.Sprintf("Unable to find target of ID(%d): %v", targetID, err)
		klog.Warning(msg)
		return nil, status.Error(codes.NotFound, msg)
	}

	if len(target.MappedLuns) < mappingIndex {
		msg := fmt.Sprintf("Target %s(%d) does not have mapping for index %d", target.Name, target.TargetID, mappingIndex)
		klog.Error(msg)
		return nil, status.Error(codes.FailedPrecondition, msg)
	}

	// Get LUN
	mapping := target.MappedLuns[mappingIndex-1]
	lun, err := cs.lunAPI.Get(mapping.LunUUID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Get request size and current size (GB)
	requestGb, err := validateCapacity(req.GetCapacityRange().GetRequiredBytes(), req.GetCapacityRange().GetLimitBytes())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	currentGb := lun.Size >> 30

	// Check whether new size is lager than current LUN size
	if requestGb <= currentGb {
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         currentGb << 30,
			NodeExpansionRequired: true,
		}, nil
	}

	// Check whether expanded size is allocatable or not in synology volume
	vol, err := cs.volumeAPI.Get(lun.Location)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	capacity, _ := strconv.ParseInt(vol.SizeFreeByte, 10, 64)
	if capacity < (requestGb<<30 - currentGb<<30) {
		msg := fmt.Sprintf("no enough space in synology volume: %d Byte left", capacity)
		return nil, status.Error(codes.Internal, msg)
	}

	// Update LUN for expanding volume
	err = cs.lunAPI.Update(lun.UUID, requestGb<<30)
	if err != nil {
		msg := fmt.Sprintf(
			"Unable to update volume: %s", lun.Name)
		klog.Error(msg)
		return nil, status.Error(codes.Internal, msg)
	}

	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requestGb << 30,
		NodeExpansionRequired: true,
	}, nil
}

// CreateVolume creates a LUN and a target for a volume
func (cs *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// Volume name
	volName := req.GetName()
	if len(volName) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Name is required")
	}

	caps := req.GetVolumeCapabilities()
	if caps == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capabilities are required")
	}

	// Keep a record of the requested access types.
	var accessTypeMount, accessTypeBlock bool
	var accessMode csi.VolumeCapability_AccessMode_Mode

	for _, cap := range caps {
		if cap.GetBlock() != nil {
			accessTypeBlock = true
		}
		if cap.GetMount() != nil {
			accessTypeMount = true
		}
		if cap.GetAccessMode() != nil {
			accessMode = cap.GetAccessMode().GetMode()
		}
	}

	if accessTypeBlock || !accessTypeMount {
		return nil, status.Errorf(codes.InvalidArgument, "Only Mount access is supported")
	}

	// Validate Access Mode
	switch accessMode {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER:
	default:
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Unsupported access mode: %s", csi.VolumeCapability_AccessMode_Mode_name[int32(accessMode)],
		)
	}

	// Volume size
	volSizeByte := defaultVolumeSize
	if req.GetCapacityRange() != nil {
		volSizeByte = int64(req.GetCapacityRange().GetRequiredBytes())
	}

	//
	// Create volumes
	//
	params := req.GetParameters()
	location, present := params["location"]
	if !present {
		location = defaultLocation
	}

	// check if location exists
	volume, err := cs.volumeAPI.Get(location)
	if err != nil {
		volumes, listErr := cs.volumeAPI.List()
		if listErr != nil {
			return nil, status.Errorf(
				codes.Internal,
				fmt.Sprintf("Unable to list storage volumes: %v", listErr))
		}

		var locations []string
		for _, vol := range volumes {
			locations = append(locations, vol.VolumePath)
		}

		return nil, status.Errorf(
			codes.InvalidArgument,
			fmt.Sprintf("Unable to find location %s, valid locations: %v", location, locations))
	}

	klog.V(1).Infof("Found the volume for the location %s: %v", location, volume)

	volType, present := params["type"]
	if !present {
		if volume.FSType == storage.FSTypeExt4 {
			volType = defaultVolumeTypeExt4
		} else if volume.FSType == storage.FSTypeBtrfs {
			volType = defaultVolumeTypeBtrfs
		}
	}

	lunName := fmt.Sprintf("%s-%s", lunNamePrefix, volName)
	targetName := fmt.Sprintf("%s-%s", targetNamePrefix, volName)
	targetIQN := fmt.Sprintf("%s-%s", iqnPrefix, volName)

	// check if lun already exists
	lun, err := cs.lunAPI.Get(lunName)
	if lun == nil {
		// create a lun
		newLun, err := cs.lunAPI.Create(
			lunName,
			location,
			volSizeByte,
			volType,
		)

		if err != nil {
			msg := fmt.Sprintf(
				"Failed to create a LUN(name: %s, location: %s, size: %d, type: %s): %v",
				lunName, location, volSizeByte, volType, err)
			klog.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}

		klog.V(1).Infof("LUN %s(%s) created", lunName, newLun.UUID)
		lun = newLun
	} else {
		klog.V(1).Infof("Volume %s already exists, found LUN %s. Will use existing LUN", volName, lunName)
	}

	var target *iscsi.Target
	if lun.IsMapped {
		// find mapped target
		targets, err := cs.targetAPI.List()
		if err != nil {
			msg := fmt.Sprintf("Failed get list of targets: %v", err)
			klog.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}

		for _, tgt := range targets {
			isMappedToLun := false
			for _, mappedLun := range tgt.MappedLuns {
				if mappedLun.LunUUID == lun.UUID {
					isMappedToLun = true
					break
				}
			}

			if isMappedToLun {
				target = &tgt
				break
			}
		}

		if target == nil {
			msg := fmt.Sprintf("Failed to find target mapped to LUN %s", lunName)
			klog.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}

	} else {
		// create a target
		secrets := req.GetSecrets()
		user, present := secrets["user"]
		if present {
			password, present := secrets["password"]
			if !present {
				klog.Warning("Password is required to provide chap authentication")
				return nil, status.Error(codes.InvalidArgument, "Password is missing")
			}
			target, err = cs.targetAPI.Create(
				targetName,
				targetIQN,
				iscsi.TargetAuthTypeNone,
				user, password,
			)
		} else {
			target, err = cs.targetAPI.Create(
				targetName,
				targetIQN,
				iscsi.TargetAuthTypeNone,
				"", "",
			)
		}

		if err != nil {
			msg := fmt.Sprintf(
				"Failed to create target(name: %s, iqn: %s): %v",
				targetName, targetIQN, err)
			klog.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}

		klog.V(1).Infof("Target %s(ID: %d) created", targetName, target.TargetID)

		// map lun
		err = cs.targetAPI.MapLun(
			target.TargetID, []string{lun.UUID})
		if err != nil {
			msg := fmt.Sprintf(
				"Failed to map LUN %s(%s) to target %s(%d): %v",
				lun.Name, lun.UUID, target.Name, target.TargetID, err)
			klog.Error(msg)
			return nil, status.Error(codes.Internal, msg)
		}

		klog.V(1).Infof("Mapped LUN %s(%s) to target %s(ID: %d)",
			lun.Name, lun.UUID, target.Name, target.TargetID)

	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      makeVolumeID(target.TargetID, 1),
			CapacityBytes: volSizeByte,
			VolumeContext: map[string]string{
				"targetID":     fmt.Sprintf("%d", target.TargetID),
				"iqn":          target.IQN,
				"mappingIndex": "1",
			},
		},
	}, nil
}

// DeleteVolume deletes the LUN and the target created for the volume
func (cs *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {

	volID := req.GetVolumeId()
	if volID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	targetID, mappingIndex, err := parseVolumeID(volID)
	if err != nil {
		klog.Warningf("invalid volumeid '%s' in DeleteVolume: %v", volID, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	target, err := cs.targetAPI.Get(targetID)
	if err != nil {
		klog.Warningf("target for ID(%d) not found: %v", targetID, err)
		return &csi.DeleteVolumeResponse{}, nil
	}

	if len(target.MappedLuns) < mappingIndex {
		msg := fmt.Sprintf("Target %s(%d) does not have mapping for index %d",
			target.Name, target.TargetID, mappingIndex)
		klog.Error(msg)
		return nil, status.Error(codes.FailedPrecondition, msg)
	}

	mapping := target.MappedLuns[mappingIndex-1]
	lun, err := cs.lunAPI.Get(mapping.LunUUID)
	if err != nil {
		msg := fmt.Sprintf(
			"Unable to find LUN of UUID: %s(mapped to target %s(%d))",
			mapping.LunUUID, target.Name, target.TargetID)
		klog.Error(msg)
		return nil, status.Error(codes.NotFound, msg)
	}

	// unmap lun
	err = cs.targetAPI.UnmapLun(target.TargetID, []string{lun.UUID})
	if err != nil {
		msg := fmt.Sprintf(
			"Failed to unmap LUN %s(%s) to target %s(%d): %v",
			lun.Name, lun.UUID, target.Name, target.TargetID, err)
		klog.Error(msg)
		return nil, status.Error(codes.Internal, msg)
	}

	klog.V(1).Infof("Unmapped LUN %s(%s) to target %s(ID: %d)",
		lun.Name, lun.UUID, target.Name, target.TargetID)

	// delete target
	err = cs.targetAPI.Delete(target.TargetID)
	if err != nil {
		msg := fmt.Sprintf(
			"Failed to delete target %s(%d): %v",
			target.Name, target.TargetID, err)
		klog.Error(msg)
		return nil, status.Error(codes.Internal, msg)
	}
	klog.V(1).Infof("Deleted target %s(%d)",
		target.Name, target.TargetID)

	// delete lun
	err = cs.lunAPI.Delete(lun.UUID)
	if err != nil {
		msg := fmt.Sprintf(
			"Failed to delete lun %s(%s): %v",
			lun.Name, lun.UUID, err)
		klog.Error(msg)
		return nil, status.Error(codes.Internal, msg)
	}
	klog.V(1).Infof("Deleted lun %s(%s)",
		lun.Name, lun.UUID)

	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// TODO(ressu): A LOT of this can be deduplicated with CreateVolume
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	caps := req.GetVolumeCapabilities()
	if caps == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capabilities are required")
	}

	// Keep a record of the requested access types.
	var accessTypeMount, accessTypeBlock bool
	var accessMode csi.VolumeCapability_AccessMode_Mode

	for _, cap := range caps {
		if cap.GetBlock() != nil {
			accessTypeBlock = true
		}
		if cap.GetMount() != nil {
			accessTypeMount = true
		}
		if cap.GetAccessMode() != nil {
			accessMode = cap.GetAccessMode().GetMode()
		}
	}

	if accessTypeBlock || !accessTypeMount {
		return nil, status.Errorf(codes.InvalidArgument, "Only Mount access is supported")
	}

	// Validate Access Mode
	switch accessMode {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY:
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER:
	default:
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Unsupported access mode: %s", csi.VolumeCapability_AccessMode_Mode_name[int32(accessMode)],
		)
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeContext:      req.GetVolumeContext(),
			VolumeCapabilities: req.GetVolumeCapabilities(),
			Parameters:         req.GetParameters(),
		},
	}, nil
}

func (cs *controllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	// TODO(ressu): this is a total cop out fix pagination
	// If we receive a pagination token, fail immediately
	if req.GetStartingToken() != "" {
		return nil, status.Error(codes.Aborted, "Invalid starting token")
	}

	targets, err := cs.targetAPI.List()
	if err != nil {
		msg := fmt.Sprintf("Failed to list targets: %v", err)
		klog.Error(msg)
		return nil, status.Error(codes.Internal, msg)
	}

	var entries []*csi.ListVolumesResponse_Entry
	for _, t := range targets {

		if !strings.HasPrefix(t.Name, targetNamePrefix) {
			// I was not able to find a good way to flag volumes created by csi
			// other than using prefix..
			continue
		}

		for _, mapping := range t.MappedLuns {
			lun, err := cs.lunAPI.Get(mapping.LunUUID)
			if err != nil {
				msg := fmt.Sprintf("Failed to get LUN(%s): %v", mapping.LunUUID, err)
				klog.Error(msg)
				return nil, status.Error(codes.Internal, msg)

			}
			if lun == nil {
				continue
			}

			entry := csi.ListVolumesResponse_Entry{
				Volume: &csi.Volume{
					VolumeId:      fmt.Sprintf("%d.%d", t.TargetID, mapping.MappingIndex),
					CapacityBytes: lun.Size,
					VolumeContext: map[string]string{
						"targetID":     fmt.Sprintf("%d", t.TargetID),
						"iqn":          t.IQN,
						"mappingIndex": fmt.Sprintf("%d", mapping.MappingIndex),
					},
				},
			}

			entries = append(entries, &entry)
		}
	}

	return &csi.ListVolumesResponse{
		Entries: entries,
	}, nil
}

func (cs *controllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: cs.driver.cscap,
	}, nil
}

func (cs *controllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	// TODO(ressu): most of NAS interactions from the node should be moved here instead
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Name is required")
	}

	if req.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Node ID is required")
	}

	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume Capabilities are required")
	}
	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (cs *controllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}

	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// Stubs for unused calls
func (cs *controllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *controllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *controllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *controllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *controllerServer) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
