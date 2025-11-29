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

import (
	"fmt"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
)

// MeasurementName extract measurement from series key,
// return measurement_name_with_version, tail, error
func MeasurementName(src []byte) ([]byte, []byte, error) {
	if len(src) < 4 {
		return nil, nil, fmt.Errorf("too small data for tags")
	}

	kl := int(encoding.UnmarshalUint32(src))
	if len(src) < kl {
		return nil, nil, fmt.Errorf("too small indexKey")
	}
	src = src[4:]

	mnl := int(encoding.UnmarshalUint16(src))
	src = src[2:]
	if mnl+2 > len(src) {
		return nil, nil, fmt.Errorf("too small data for measurement(%d: %d > %d)", kl, mnl, len(src))
	}
	mn := src[:mnl]
	src = src[mnl:]

	return mn, src, nil
}

func GetOriginMstName(nameWithVer string) string {
	if len(nameWithVer) < 5 {
		// test case tolerate
		return nameWithVer
	}
	if nameWithVer[len(nameWithVer)-5] == '_' &&
		nameWithVer[len(nameWithVer)-4] >= '0' && nameWithVer[len(nameWithVer)-4] <= '9' &&
		nameWithVer[len(nameWithVer)-3] >= '0' && nameWithVer[len(nameWithVer)-3] <= '9' &&
		nameWithVer[len(nameWithVer)-2] >= '0' && nameWithVer[len(nameWithVer)-2] <= '9' &&
		nameWithVer[len(nameWithVer)-1] >= '0' && nameWithVer[len(nameWithVer)-1] <= '9' {
		return nameWithVer[:len(nameWithVer)-5]
	}
	return nameWithVer
}

func FieldTypeString(fieldType int32) string {
	switch fieldType {
	case Field_Type_Int:
		return "integer"
	case Field_Type_UInt:
		return "unsigned"
	case Field_Type_Float:
		return "float"
	case Field_Type_String:
		return "string"
	case Field_Type_Boolean:
		return "boolean"
	case Field_Type_Tag:
		return "tag"
	default:
		return "unknown"
	}
}
