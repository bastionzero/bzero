// Copyright 2022 BastionZero Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// https://www.apache.org/licenses/LICENSE-2.0
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package config

const (

	// Buffer capacity of 100000 items with each buffer item of 1024 bytes leads to max usage of 100MB (100000 * 1024 bytes = 100MB) of instance memory.
	// When changing StreamDataPayloadSize, make corresponding change to buffer capacity to ensure no more than 100MB of instance memory is used.
	StreamDataPayloadSize         = 1024
	OutgoingMessageBufferCapacity = 100 * 1000
	IncomingMessageBufferCapacity = 100 * 1000
	ShellStdOutBuffCapacity       = 10 * 1000

	// ExitCodes
	ErrorExitCode = 1
)
