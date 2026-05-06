#!/bin/bash -xe

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


OPERATOR_NAME=$1
VALUES_FILE_NAME=${2:-values.yaml}

## need to specify different paths for the helm binary
## depending on whether or not this is being ran in CI
if [ -n "$GITLAB_CI" ]; then
    HELM=/workspace/bin/helm
else 
    HELM=$(which helm)
fi

LOCAL_OPERATOR_IMG_ARGS=()
if [ -n "${LOCAL_OPERATOR_IMG:-}" ]; then
    LOCAL_OPERATOR_IMG_WITHOUT_DIGEST=${LOCAL_OPERATOR_IMG%%@*}
    LOCAL_OPERATOR_IMG_DIGEST=""
    if [[ "${LOCAL_OPERATOR_IMG}" == *@* ]]; then
        LOCAL_OPERATOR_IMG_DIGEST=${LOCAL_OPERATOR_IMG#*@}
    fi

    LOCAL_OPERATOR_IMG_REPOSITORY=${LOCAL_OPERATOR_IMG_WITHOUT_DIGEST}
    LOCAL_OPERATOR_IMG_TAG=""
    LOCAL_OPERATOR_IMG_LAST_PART=${LOCAL_OPERATOR_IMG_WITHOUT_DIGEST##*/}
    if [[ "${LOCAL_OPERATOR_IMG_LAST_PART}" == *:* ]]; then
        LOCAL_OPERATOR_IMG_TAG=${LOCAL_OPERATOR_IMG_LAST_PART##*:}
        LOCAL_OPERATOR_IMG_REPOSITORY=${LOCAL_OPERATOR_IMG_WITHOUT_DIGEST%:*}
    fi

    LOCAL_OPERATOR_IMG_ARGS=(
        --set "controllerManager.manager.image.repository=${LOCAL_OPERATOR_IMG_REPOSITORY}"
        --set "controllerManager.manager.image.tag=${LOCAL_OPERATOR_IMG_TAG}"
        --set "controllerManager.manager.image.digest=${LOCAL_OPERATOR_IMG_DIGEST}"
    )
fi

## install operator
${HELM} upgrade --install ${OPERATOR_NAME} ../../../../chart -n skyhook -f ${VALUES_FILE_NAME} "${LOCAL_OPERATOR_IMG_ARGS[@]}"
