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

package shell

type ShellAction string

const (
	DefaultShell ShellAction = "default"
)

type ShellActionParams struct {
	Attach     bool   `json:"attach"`
	TargetUser string `json:"targetUser"`
}

type ShellOpenMessage struct{}

type ShellCloseMessage struct{}

type ShellInputMessage struct {
	Data []byte `json:"data"`
}

type ShellResizeMessage struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

type ShellReplayMessage struct{}

type ShellSubAction string

const (
	ShellOpen   ShellSubAction = "shell/open"
	ShellClose  ShellSubAction = "shell/close"
	ShellResize ShellSubAction = "shell/resize"
	ShellInput  ShellSubAction = "shell/input"
	ShellReplay ShellSubAction = "shell/replay"
)
