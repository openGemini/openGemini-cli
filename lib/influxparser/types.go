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
	"strings"

	"github.com/openGemini/openGemini/lib/errno"
)

var (
	// ErrPointMustHaveAField is returned when operating on a point that does not have any fields.
	ErrPointMustHaveAField = errno.NewError(errno.WritePointMustHaveAField)
	// ErrInvalidPoint is returned when a point cannot be parsed correctly.
	ErrInvalidPoint = errno.NewError(errno.WriteInvalidPoint)
)

const (
	Field_Type_Unknown = 0
	Field_Type_Int     = 1
	Field_Type_UInt    = 2
	Field_Type_Float   = 3
	Field_Type_String  = 4
	Field_Type_Boolean = 5
	Field_Type_Tag     = 6
	Field_Type_Last    = 7
)

const ByteSplit = 0x00

// Row is a single influx row.
type Row struct {
	// if streamOnly is false, it means that the source table data of the stream will also be written,
	// otherwise the source table data of the stream will not be written
	StreamOnly              bool
	Timestamp               int64
	SeriesId                uint64
	PrimaryId               uint64
	Name                    string // measurement name with version
	Tags                    PointTags
	Fields                  Fields
	IndexKey                []byte
	ShardKey                []byte
	StreamId                []uint64 // it used to indicate that the data is shared by multiple streams
	IndexOptions            IndexOptions
	ColumnToIndex           map[string]int // it indicates the sorted tagKey, fieldKey and index mapping relationship
	ReadyBuildColumnToIndex bool

	tagArrayInitialized bool
	hasTagArray         bool
	skipMarshalShardKey bool
}

func (r *Row) Reset() {
	r.Name = ""
	r.Tags = nil
	r.Fields = nil
	r.IndexKey = nil
	r.ShardKey = nil
	r.IndexOptions = nil
	r.StreamId = nil
	r.StreamOnly = false
	r.Timestamp = 0
	r.SeriesId = 0
	r.PrimaryId = 0
	r.ColumnToIndex = nil
	r.ReadyBuildColumnToIndex = false
	r.tagArrayInitialized = false
	r.hasTagArray = false
	r.skipMarshalShardKey = false
}

// Tag PointTag represents influx tag.
type Tag struct {
	Key     string
	Value   string
	IsArray bool
}

func (tag *Tag) Reset() {
	tag.Key = ""
	tag.Value = ""
	tag.IsArray = false
}

func (tag *Tag) Size() int {
	return len(tag.Key) + len(tag.Value)
}

type PointTags []Tag

func (pts *PointTags) Less(i, j int) bool {
	x := *pts
	return x[i].Key < x[j].Key
}
func (pts *PointTags) Len() int { return len(*pts) }
func (pts *PointTags) Swap(i, j int) {
	x := *pts
	x[i], x[j] = x[j], x[i]
}

func (pts *PointTags) TagsSize() int {
	var total int
	for i := range *pts {
		total += (*pts)[i].Size()
	}
	return total
}

func (pts *PointTags) Reset() {
	for i := range *pts {
		(*pts)[i].Reset()
	}
}

func (pts *PointTags) HasTagArray() bool {
	has := false
	for i := 0; i < len(*pts); i++ {
		val := (*pts)[i].Value
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			(*pts)[i].IsArray = true
			has = true
		}
	}
	return has
}

// Field represents influx field.
type Field struct {
	Key      string
	NumValue float64
	StrValue string
	Type     int32
}

func (f *Field) Reset() {
	f.Key = ""
	f.NumValue = 0
	f.StrValue = ""
	f.Type = Field_Type_Unknown
}

type Fields []Field

func (fs *Fields) Less(i, j int) bool {
	return (*fs)[i].Key < (*fs)[j].Key
}

func (fs *Fields) Len() int {
	return len(*fs)
}

func (fs *Fields) Swap(i, j int) {
	(*fs)[i], (*fs)[j] = (*fs)[j], (*fs)[i]
}

func (fs *Fields) Reset() {
	for i := range *fs {
		(*fs)[i].Reset()
	}
}

// IndexOption represents index option.
type IndexOption struct {
	IndexList []uint16
	Oids      []uint32
	IndexType uint32
	Oid       uint32
}

type IndexOptions []IndexOption
