#!/bin/bash

# SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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




node=$1
cmd=$2
check=$3
timeout=${4:-10}
invert=${5:-false}


# loop until the check passes or the retry budget is spent
for i in $(seq 1 ${timeout}); do
    data=$(kubectl exec ${node}-debugger -- chroot /host bash -c "${cmd}")
    status=$?
    check_result=$(echo "${data}" | grep -c "${check}")
    if [ "$invert" == "true" ]; then
        check_result=$((! check_result))
    elif [ $status -ne 0 ]; then
        ## A positive match only counts if the command itself succeeded: `cat a.log b.log`
        ## prints a.log and exits non-zero when b.log is unreadable, and that output must
        ## not pass a check. Inverted checks assert absence, where a command with nothing
        ## to list exits non-zero by design, so they keep matching on output alone.
        check_result=0
    fi
    if [ $check_result -gt 0 ]; then
        echo "Check passed"
        exit 0
    else
        echo "Check failed"
    fi
    sleep 1
done
echo "Data: ${data}"
echo "Exit: ${status}"
echo "Check: ${check}"

exit 1
