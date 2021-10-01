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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"
)

func makeVolumeID(targetID int, mappingIndex int) string {
	return fmt.Sprintf("%d.%d", targetID, mappingIndex)
}

func parseVolumeID(volumeID string) (int, int, error) {
	tokens := strings.Split(volumeID, ".")

	if len(tokens) != 2 {
		return 0, 0, fmt.Errorf(
			"Invalid volume ID: %s", volumeID)
	}

	targetID, err := strconv.Atoi(tokens[0])
	if err != nil {
		return 0, 0, fmt.Errorf(
			"Invalid volume ID: %s", volumeID)
	}
	mappingIndex, err := strconv.Atoi(tokens[1])
	if err != nil {
		return 0, 0, fmt.Errorf(
			"Invalid volume ID: %s", volumeID)
	}

	return targetID, mappingIndex, nil
}

func validateCapacity(requestBytes, limitBytes int64) (int64, error) {
	if requestBytes < 0 {
		return 0, errors.New("request bytes must be positive integer")
	}
	if limitBytes < 0 {
		return 0, errors.New("limit bytes must be positive integer")
	}

	if limitBytes != 0 && requestBytes > limitBytes {
		return 0, fmt.Errorf(
			"request must less than limit: request=%d limit=%d", requestBytes, limitBytes,
		)
	}

	if requestBytes == 0 {
		return 1, nil
	}
	return (requestBytes-1)>>30 + 1, nil
}

func newDefaultIdentityServer(d *driver) *identityServer {
	return &identityServer{
		driver: d,
	}
}

func newControllerServiceCapability(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}

func ParseEndpoint(ep string) (string, string, error) {
	if strings.HasPrefix(strings.ToLower(ep), "unix://") || strings.HasPrefix(strings.ToLower(ep), "tcp://") {
		s := strings.SplitN(ep, "://", 2)
		if s[1] != "" {
			return s[0], s[1], nil
		}
	}
	return "", "", fmt.Errorf("Invalid endpoint: %v", ep)
}

func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	klog.V(1).Infof("GRPC call: %s", info.FullMethod)
	klog.V(2).Infof("GRPC request: %s", protosanitizer.StripSecrets(req))
	resp, err := handler(ctx, req)
	if err != nil {
		klog.Errorf("GRPC error: %v", err)
	} else {
		klog.V(2).Infof("GRPC response: %s", protosanitizer.StripSecrets(resp))
	}
	return resp, err
}

// IscsiLogWriter is an iscsi-lib compatible logging writer
//
// Usage:
//   if klog.V(2).Enabled() {
//     iscsi.EnableDebugLogging(IscsiLogWriter{})
//   }
type IscsiLogWriter struct{}

func (w IscsiLogWriter) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	klog.InfoDepth(3, string(b))
	return 0, nil
}
