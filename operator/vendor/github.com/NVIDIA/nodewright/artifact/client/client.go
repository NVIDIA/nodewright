// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package client reads NodeWright package artifacts from an OCI registry or
// an OCI image layout. It is a subpackage because it touches the network and
// the filesystem, which the root artifact package never does.
package client

import "errors"

// Target errors that fall into one of these classes match it under errors.Is
// and still wrap the error they classify. An error from a caller context that
// is done matches ctx.Err() and none of them.
var (
	ErrNotFound       = errors.New("not found")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrTransport      = errors.New("transport failure")
	ErrDigestMismatch = errors.New("content does not match its descriptor")
)
