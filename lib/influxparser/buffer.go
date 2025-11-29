// Copyright 2025 openGemini Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package influxparser

import "sync"

var bPool = &sync.Pool{}

func GetBytesBuffer() []byte {
	v := bPool.Get()
	if v != nil {
		return v.([]byte)
	}
	return []byte{}
}

func PutBytesBuffer(b []byte) {
	b = b[:0]
	bPool.Put(b) //nolint:staticcheck // SA6002: []byte is already a reference type
}
