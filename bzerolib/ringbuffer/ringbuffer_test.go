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

package ringbuffer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmptyRingBuffer(t *testing.T) {
	rb := New(0)
	assert.NotNil(t, rb)

	buff := make([]byte, 10)

	n, err := rb.Read(buff)
	assert.Nil(t, err)
	assert.EqualValues(t, 0, n)
}

func TestSimpleWriteReads(t *testing.T) {
	wbuff0 := []byte{}
	wbuff2 := []byte{0x11, 0x12}
	wbuff3 := []byte{0xa, 0xb, 0xc}

	wbuff9 := []byte{0x1, 0x2, 0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9}

	rbuff0 := make([]byte, 1)
	rbuff1 := make([]byte, 1)
	rbuff5 := make([]byte, 5)
	rbuff7 := make([]byte, 7)

	rblen := 7
	rb := New(rblen)
	assert.NotNil(t, rb)

	n, err := rb.Read(rbuff0)
	assert.Nil(t, err)
	assert.EqualValues(t, 0, n)

	n, err = rb.Read(rbuff7)
	assert.Nil(t, err)
	assert.EqualValues(t, 0, n)
	assert.EqualValues(t, []byte{0x0, 0x0, 0x0, 0x0, 0x0, 0x0, 0x0}, rbuff7)

	n, err = rb.Write(wbuff9)
	assert.Nil(t, err)
	assert.EqualValues(t, rblen, n)

	n, err = rb.Read(rbuff7)
	assert.EqualValues(t, 7, n)
	assert.EqualValues(t, []byte{0x3, 0x4, 0x5, 0x6, 0x7, 0x8, 0x9}, rbuff7)

	n, err = rb.Read(rbuff1)
	assert.EqualValues(t, 1, n)
	assert.EqualValues(t, []byte{0x3}, rbuff1)

	n, err = rb.Write(wbuff3)
	assert.Nil(t, err)
	assert.EqualValues(t, 3, n)

	n, err = rb.Write(wbuff0)
	assert.Nil(t, err)
	assert.EqualValues(t, 0, n)

	n, err = rb.Read(rbuff5)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x6, 0x7, 0x8, 0x9, 0xa}, rbuff5)

	n, err = rb.Write(wbuff2)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)

	n, err = rb.Read(rbuff7)
	assert.EqualValues(t, 7, n)
	assert.EqualValues(t, []byte{0x8, 0x9, 0xa, 0xb, 0xc, 0x11, 0x12}, rbuff7)

	n, err = rb.Write(wbuff0)
	assert.Nil(t, err)
	assert.EqualValues(t, 0, n)

	n, err = rb.Read(rbuff7)
	assert.EqualValues(t, 7, n)
	assert.EqualValues(t, []byte{0x8, 0x9, 0xa, 0xb, 0xc, 0x11, 0x12}, rbuff7)
}

func TestEdgeCases(t *testing.T) {
	wbuff1 := []byte{0x01}
	wbuff2 := []byte{0x11, 0x12}
	wbuff3 := []byte{0x21, 0x22, 0x23}
	wbuff4 := []byte{0x31, 0x32, 0x33, 0x34}
	wbuff5 := []byte{0x41, 0x42, 0x43, 0x44, 0x45}
	wbuff6 := []byte{0x51, 0x52, 0x53, 0x54, 0x55, 0x56}

	rbuff1 := make([]byte, 1)
	rbuff2 := make([]byte, 2)
	rbuff3 := make([]byte, 3)
	rbuff4 := make([]byte, 4)
	rbuff5 := make([]byte, 5)
	rbuff6 := make([]byte, 6)

	rblen := 5
	rb := New(rblen)
	assert.NotNil(t, rb)

	// write 0x11, 0x12 --> (head)0x11, 0x12, (tail)0x00, 0x00, 0x00
	n, err := rb.Write(wbuff2)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff2), n)

	n, err = rb.Read(rbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, 1, n)
	assert.EqualValues(t, []byte{0x11}, rbuff1)

	n, err = rb.Read(rbuff2)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)
	assert.EqualValues(t, []byte{0x11, 0x12}, rbuff2)

	n, err = rb.Read(rbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x00, 0x00}, rbuff4)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x00, 0x00, 0x00}, rbuff5)

	n, err = rb.Read(rbuff6)
	assert.Nil(t, err)
	assert.EqualValues(t, 2, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x00, 0x00, 0x00, 0x00}, rbuff6)

	// write 0x01 --> (head)0x11, 0x12, 0x01, (tail)0x00, 0x00
	n, err = rb.Write(wbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff1), n)

	n, err = rb.Read(rbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, 1, n)
	assert.EqualValues(t, []byte{0x11}, rbuff1)

	n, err = rb.Read(rbuff3)
	assert.Nil(t, err)
	assert.EqualValues(t, 3, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01}, rbuff3)

	n, err = rb.Read(rbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, 3, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x00}, rbuff4)

	// write 0x01 --> (head)0x11, 0x12, 0x01, 0x01, (tail)0x00
	n, err = rb.Write(wbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff1), n)

	n, err = rb.Read(rbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, 4, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x01}, rbuff4)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 4, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x01, 0x00}, rbuff5)

	n, err = rb.Read(rbuff6)
	assert.Nil(t, err)
	assert.EqualValues(t, 4, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x01, 0x00, 0x00}, rbuff6)

	// write 0x01 --> (tail)(head)0x11, 0x12, 0x01, 0x01, 0x01
	n, err = rb.Write(wbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff1), n)

	n, err = rb.Read(rbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, 4, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x01}, rbuff4)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x01, 0x01}, rbuff5)

	n, err = rb.Read(rbuff6)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x11, 0x12, 0x01, 0x01, 0x01, 0x00}, rbuff6)

	// write 0x01 --> 0x01, (tail)(head)0x12, 0x01, 0x01, 0x01
	n, err = rb.Write(wbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff1), n)

	n, err = rb.Read(rbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, 4, n)
	assert.EqualValues(t, []byte{0x12, 0x01, 0x01, 0x01}, rbuff4)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x12, 0x01, 0x01, 0x01, 0x01}, rbuff5)

	// write 0x21, 0x22, 0x33, --> 0x01, 0x21, 0x22, 0x23, (tail)(head)0x01
	n, err = rb.Write(wbuff3)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff3), n)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x01, 0x01, 0x21, 0x22, 0x23}, rbuff5)

	n, err = rb.Read(rbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, 1, n)
	assert.EqualValues(t, []byte{0x01}, rbuff1)

	// write 0x41, 0x42, 0x43, 0x44, 0x45 --> 0x42, 0x43, 0x44, 0x45, (tail)(head)0x41
	n, err = rb.Write(wbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff5), n)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x41, 0x42, 0x43, 0x44, 0x45}, rbuff5)

	n, err = rb.Read(rbuff1)
	assert.Nil(t, err)
	assert.EqualValues(t, 1, n)
	assert.EqualValues(t, []byte{0x41}, rbuff1)

	// write 0x31, 0x32, 0x33, 0x34 --> 0x32, 0x33, 0x34, (tail)(head)0x45, 0x31
	n, err = rb.Write(wbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, len(wbuff4), n)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x45, 0x31, 0x32, 0x33, 0x34}, rbuff5)

	n, err = rb.Read(rbuff4)
	assert.Nil(t, err)
	assert.EqualValues(t, 4, n)
	assert.EqualValues(t, []byte{0x45, 0x31, 0x32, 0x33}, rbuff4)

	// write 0x51, 0x52, 0x53, 0x54, 0x55, 0x56 --> 0x53, 0x54, 0x55, 0x56, (tail)(head)0x52
	n, err = rb.Write(wbuff6)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)

	n, err = rb.Read(rbuff5)
	assert.Nil(t, err)
	assert.EqualValues(t, 5, n)
	assert.EqualValues(t, []byte{0x52, 0x53, 0x54, 0x55, 0x56}, rbuff5)
}
