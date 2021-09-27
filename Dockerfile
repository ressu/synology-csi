#
# Copyright 2018 Ji-Young Park(jiyoung.park.dev@gmail.com)
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

# Alpine is provided for different architectures, amd64, arm32 and arm64
FROM alpine:3.14

LABEL maintainers="Kubernetes Authors"
LABEL description="Synology CSI Plugin"

RUN apk add --no-cache e2fsprogs e2fsprogs-extra xfsprogs xfsprogs-extra util-linux iproute2 blkid open-iscsi
COPY synology-csi-driver /synology-csi-driver

ENTRYPOINT ["/synology-csi-driver"]
