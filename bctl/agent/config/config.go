// Copyright 2018 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may not
// use this file except in compliance with the License. A copy of the
// License is located at
//
// http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing
// permissions and limitations under the License.

// This code has been modified from the code covered by the Apache License 2.0.
// Modifications Copyright (C) 2022 BastionZero Inc.  The BastionZero Agent
// is licensed under the Apache 2.0 License.

package config

const (

	// Buffer capacity of 100000 items with each buffer item of 1024 bytes leads to max usage of 100MB (100000 * 1024 bytes = 100MB) of instance memory.
	// When changing StreamDataPayloadSize, make corresponding change to buffer capacity to ensure no more than 100MB of instance memory is used.
	StreamDataPayloadSize         = 1024
	OutgoingMessageBufferCapacity = 100000
	IncomingMessageBufferCapacity = 100000

	// ExitCodes
	SuccessExitCode = 0
	ErrorExitCode   = 1
)
