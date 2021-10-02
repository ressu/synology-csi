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
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/klog/v2"

	"github.com/jparklab/synology-csi/pkg/synology/api/iscsi"
	"github.com/jparklab/synology-csi/pkg/synology/api/storage"
	"github.com/jparklab/synology-csi/pkg/synology/core"
	"github.com/jparklab/synology-csi/pkg/synology/options"
)

const (
	// DriverName is the name of csi driver for synology
	DriverName = "csi.synology.com"
)

// Driver is top interface to run server
type Driver interface {
	Run()
}

type driver struct {
	name    string
	nodeID  string
	version string

	endpoint string

	synologyHost string
	session      core.Session

	cap   []*csi.VolumeCapability_AccessMode
	cscap []*csi.ControllerServiceCapability
}

func Login(synoOption *options.SynologyOptions) (*core.Session, string, error) {
	var proto = "http"
	if synoOption.SslVerify {
		proto = "https"
	}

	synoAPIUrl := fmt.Sprintf(
		"%s://%s:%d/webapi", proto,
		synoOption.Host, synoOption.Port)

	klog.V(1).Infof("Use Synology: %s", synoAPIUrl)

	session := core.NewSession(synoAPIUrl, synoOption.SessionName)
	loginResult, err := session.Login(synoOption)

	return &session, loginResult, err
}

// NewDriver creates a Driver object
func NewDriver(nodeID, endpoint, version string, synoOption *options.SynologyOptions) (Driver, error) {
	klog.Infof("Driver: %v", DriverName)

	session, _, err := Login(synoOption)
	if err != nil {
		klog.Errorf("Failed to login: %v", err)
		return nil, err
	}

	d := &driver{
		name:    DriverName,
		nodeID:  nodeID,
		version: version,

		endpoint:     endpoint,
		synologyHost: synoOption.Host,
		session:      *session,
	}

	d.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
			csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		},
	)

	d.AddVolumeCapabilityAccessModes(
		[]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
	)

	return d, nil
}

func (d *driver) Run() {
	s := NewNonBlockingGRPCServer()
	s.Start(d.endpoint,
		newDefaultIdentityServer(d),
		newControllerServer(d),
		newNodeServer(d))
	s.Wait()
}

func (d *driver) AddVolumeCapabilityAccessModes(vc []csi.VolumeCapability_AccessMode_Mode) []*csi.VolumeCapability_AccessMode {
	var vca []*csi.VolumeCapability_AccessMode
	for _, c := range vc {
		klog.Infof("Enabling volume access mode: %v", c.String())
		vca = append(vca, &csi.VolumeCapability_AccessMode{Mode: c})
	}
	d.cap = vca
	return vca
}

func (d *driver) AddControllerServiceCapabilities(cl []csi.ControllerServiceCapability_RPC_Type) {
	var csc []*csi.ControllerServiceCapability

	for _, c := range cl {
		klog.Infof("Enabling controller service capability: %v", c.String())
		csc = append(csc, newControllerServiceCapability(c))
	}

	d.cscap = csc

	return
}

func newControllerServer(d *driver) *controllerServer {
	klog.Infof("Create controller: %v", d)
	return &controllerServer{
		driver:    d,
		lunAPI:    iscsi.NewLunAPI(d.session),
		targetAPI: iscsi.NewTargetAPI(d.session),
		volumeAPI: storage.NewVolumeAPI(d.session),
	}
}

func newNodeServer(d *driver) *nodeServer {
	return &nodeServer{
		driver:       d,
		lunAPI:       iscsi.NewLunAPI(d.session),
		targetAPI:    iscsi.NewTargetAPI(d.session),
		iscsiPortals: []string{d.synologyHost},
	}
}
