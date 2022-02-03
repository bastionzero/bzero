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
	"fmt"
)

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

// RingBuffer this implements fixed size ringbuffer a.k.a. a cyclic buffer.
type RingBuffer struct {
	buffer     []byte
	size       int
	start, end int
	full       bool
}

// New is the RingBuffer constructor
func New(size int) *RingBuffer {
	var rb = RingBuffer{
		buffer: make([]byte, size),
		size:   size,
		start:  0,
		end:    0,
		full:   false,
	}
	return &rb
}

func (r *RingBuffer) sanitycheck() (err error) {
	if r.start >= r.size {
		return fmt.Errorf("Ringbuffer sanity check failed (start > size) start=%d, end=%d, size=%d", r.start, r.end, r.size)
	}
	if r.end > r.size {
		return fmt.Errorf("Ringbuffer sanity check failed (end > size) start=%d, end=%d, size=%d", r.start, r.end, r.size)
	}
	if r.start < 0 {
		return fmt.Errorf("Ringbuffer sanity check failed (start < 0) start=%d, end=%d, size=%d", r.start, r.end, r.size)
	}
	if r.end < 0 {
		return fmt.Errorf("Ringbuffer sanity check failed (end < 0) start=%d, end=%d, size=%d", r.start, r.end, r.size)
	}
	if r.size < 0 {
		return fmt.Errorf("Ringbuffer sanity check failed (size < 0) start=%d, end=%d, size=%d", r.start, r.end, r.size)
	}

	// All checks pass
	return nil
}

func (r *RingBuffer) Read(p []byte) (n int, err error) {
	r.sanitycheck()

	// There are two cases:
	if r.full == false {
		// Case 1 - The head hasn't eaten it's own tail yet (full == false)
		//  data in buffer less than buffer size
		//   --> r.start < r.end < r.size
		//   --> r.start = 0
		//   --> datalen = r.size-r.end = r.end - r.start
		n = min(len(p), r.end-r.start) // Don't try to read more data than exists in the buffer
	} else {
		// Case 2 - The head has eaten it's own tail (full == true)
		//  data in buffer equal to buffer size
		//   --> r.start == r.end
		//   --> datalen == r.size
		n = min(len(p), r.size)
	}

	readstop := min((r.start + n), r.size)
	copy(p[:(readstop-r.start)], r.buffer[r.start:readstop])

	// if we have more to read wrap back around
	if remainder := n - (readstop - r.start); remainder > 0 {
		copy(p[(readstop-r.start):], r.buffer[0:remainder])
	}

	return n, nil
}

func (r *RingBuffer) Write(p []byte) (n int, err error) {
	r.sanitycheck()
	n = len(p)

	// Buffer will be full when write completes
	if r.full == false && (r.end+n) >= r.size {
		r.full = true
	}

	// Optimization: if bytes to write larger than the ringbuffer, skip the writing bytes that will be overwritten.
	if n >= r.size {
		copy(r.buffer[:], p[n-r.size:]) // just write the last r.size bytes
		r.start = 0                     // reset the ringbuffer to start from the begining
		r.end = 0
		return r.size, nil
	}

	writestop := min((r.end + n), r.size)
	copy(r.buffer[r.end:writestop], p[:writestop-r.end])

	// if we have more to read wrap back around
	if remainder := max(0, n-(writestop-r.end)); remainder > 0 {
		copy(r.buffer[:remainder], p[writestop-r.end:])
	}

	r.end = (r.end + n) % r.size
	if r.full == true {
		// head eats tail
		r.start = r.end
	}

	return n, nil
}
