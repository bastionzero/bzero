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

type ShellOpenMessage struct {
	RunAsUser string `json:"runasuser"`
}

type ShellCloseMessage struct{}

type ShellInputMessage struct {
	Data string `json:"data"`
}

type ShellResizeMessage struct {
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

type ShellReplayMessage struct{}

type ShellAction string

const (
	ShellOpen    ShellAction = "open"
	ShellClose   ShellAction = "close"
	ShellResize  ShellAction = "resize"
	ShellInput   ShellAction = "input"
	ShelllReplay ShellAction = "replay"
)
