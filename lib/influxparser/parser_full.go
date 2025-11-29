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

//go:build full
// +build full

package influxparser

import (
	"errors"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/bytesutil"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
	"github.com/openGemini/openGemini/lib/logger"
	"github.com/openGemini/openGemini/lib/numberenc"
	"go.uber.org/zap"
)

const (
	INDEXCOUNT = 1
)

var hasIndexOption byte = 'y'
var hasNoIndexOption byte = 'n'

func FastUnmarshalMultiRows(src []byte, rows []Row, tagPool []Tag, fieldPool []Field, indexOptionPool []IndexOption,
	indexKeyPool []byte) ([]Row, []Tag, []Field, []IndexOption, []byte, error) {
	pointsN := int(encoding.UnmarshalUint32(src))
	src = src[4:]
	src = src[1:] // skip version

	if pointsN > cap(rows) {
		rows = make([]Row, pointsN)
	}
	rows = rows[:pointsN]

	var err error
	var decodeN int
	for len(src) > 0 {
		if decodeN >= pointsN {
			logger.GetLogger().Error("FastUnmarshalMultiRows over", zap.Int("decodeN", decodeN), zap.Int("pointsN", pointsN))
			break
		}
		row := &rows[decodeN]
		decodeN++

		row.StreamOnly = false
		src, tagPool, fieldPool, indexOptionPool, indexKeyPool, err =
			row.FastUnmarshalBinary(src, tagPool, fieldPool, indexOptionPool, indexKeyPool)
		if err != nil {
			return rows[:0], tagPool, fieldPool, indexOptionPool, indexKeyPool, err
		}
	}

	if decodeN != pointsN {
		return rows[:0], tagPool, fieldPool, indexOptionPool, indexKeyPool, errors.New("unmarshal error len(rows) != pointsN")
	}
	return rows, tagPool, fieldPool, indexOptionPool, indexKeyPool, nil
}

func (r *Row) FastUnmarshalBinary(src []byte, tagpool []Tag, fieldpool []Field, indexOptionPool []IndexOption, indexKeypool []byte) ([]byte, []Tag, []Field, []IndexOption, []byte, error) {
	if len(src) < 1 {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, errors.New("too small bytes for row binary")
	}
	var err error

	mLen := int(src[0])
	src = src[1:]
	if len(src) < mLen+4 {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, errors.New("too small bytes for row measurement")
	}
	r.Name = bytesutil.ToUnsafeString(src[:mLen])
	src = src[mLen:]

	skLen := encoding.UnmarshalUint32(src)
	src = src[4:]
	if len(src) < int(skLen+4) {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, errors.New("too small bytes for row shardKey")
	}
	r.ShardKey = append(r.ShardKey[:0], src[:skLen]...)
	src = src[skLen:]

	src, tagpool, err = r.unmarshalTags(src, tagpool)
	if err != nil {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, err
	}
	if len(src) < 4 {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, errors.New("too small bytes for row field count")
	}

	src, fieldpool, err = r.unmarshalFields(src, fieldpool)
	if err != nil {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, err
	}

	src, indexOptionPool, err = r.unmarshalIndexOptions(src, indexOptionPool)
	if err != nil {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, err
	}

	r.Timestamp = encoding.UnmarshalInt64(src[:8])
	if len(src) < 8 {
		return nil, tagpool, fieldpool, indexOptionPool, indexKeypool, errors.New("too small bytes for row timestamp")
	}

	indexKeypool = r.UnmarshalIndexKeys(indexKeypool)

	return src[8:], tagpool, fieldpool, indexOptionPool, indexKeypool, nil
}

func (r *Row) unmarshalTags(src []byte, tagpool []Tag) ([]byte, []Tag, error) {
	tagN := int(encoding.UnmarshalUint32(src[:4]))
	src = src[4:]
	start := len(tagpool)

	if len(tagpool)+tagN > cap(tagpool) {
		tagpool = append(tagpool[:cap(tagpool)], make([]Tag, start+tagN-cap(tagpool))...)
	}
	tagpool = tagpool[:start+tagN]

	for i := 0; i < tagN; i++ {
		if len(src) < 1 {
			return nil, tagpool, errors.New("too small bytes for row tag key len")
		}
		tl := int(encoding.UnmarshalUint16(src[:2])) //int(src[0])
		src = src[2:]
		if len(src) < tl+1 {
			return nil, tagpool, errors.New("too small bytes for row tag key")
		}

		tg := &tagpool[start+i]

		tg.Key = bytesutil.ToUnsafeString(src[:tl])
		src = src[tl:]
		vl := int(encoding.UnmarshalUint16(src[:2])) //int(src[0])
		if len(src) < vl {
			tagpool = tagpool[:len(tagpool)-1]
			return nil, tagpool, errors.New("too small bytes for row tag value")
		}
		src = src[2:]
		tg.Value = bytesutil.ToUnsafeString(src[:vl])
		tg.IsArray = false
		src = src[vl:]
	}
	r.Tags = tagpool[start:]
	return src, tagpool, nil
}

func (r *Row) unmarshalFields(src []byte, fieldpool []Field) ([]byte, []Field, error) {
	fieldN := int(encoding.UnmarshalUint32(src[:4]))
	src = src[4:]
	start := len(fieldpool)

	if len(fieldpool)+fieldN > cap(fieldpool) {
		fieldpool = append(fieldpool[:cap(fieldpool)], make([]Field, start+fieldN-cap(fieldpool))...)
	}
	fieldpool = fieldpool[:start+fieldN]

	for i := 0; i < fieldN; i++ {
		if len(src) < 2 {
			return nil, fieldpool, errors.New("too small for field key length")
		}
		l := int(encoding.UnmarshalUint16(src[:2])) //int(src[0])
		src = src[2:]
		if len(src) < l+1 {
			return nil, fieldpool, errors.New("too small for field key")
		}

		fd := &fieldpool[start+i]

		fd.Key = bytesutil.ToUnsafeString(src[:l])
		src = src[l:]

		fd.Type = int32(src[0])
		if fd.Type <= Field_Type_Unknown || fd.Type >= Field_Type_Last {
			fieldpool = fieldpool[:len(fieldpool)-1]
			return nil, fieldpool, errors.New("error field type")
		}
		src = src[1:]

		if fd.Type == Field_Type_String {
			if len(src) < 8 {
				fieldpool = fieldpool[:len(fieldpool)-1]
				return nil, fieldpool, errors.New("too small for string field length")
			}
			l = int(encoding.UnmarshalUint64(src[:8]))
			src = src[8:]
			if len(src) < l {
				fieldpool = fieldpool[:len(fieldpool)-1]
				return nil, fieldpool, errors.New("too small for string field value")
			}
			fd.StrValue = bytesutil.ToUnsafeString(src[:l])
			src = src[l:]
		} else {
			if len(src) < 8 {
				fieldpool = fieldpool[:len(fieldpool)-1]
				return nil, fieldpool, errors.New("too small for field")
			}
			fd.NumValue = numberenc.UnmarshalFloat64(src[:8])
			src = src[8:]
		}
	}
	r.Fields = fieldpool[start:]
	return src, fieldpool, nil
}

func (r *Row) unmarshalIndexOptions(src []byte, indexOptionPool []IndexOption) ([]byte, []IndexOption, error) {
	isIndexOpt := src[:INDEXCOUNT]
	r.IndexOptions = nil
	if isIndexOpt[0] == hasNoIndexOption {
		src = src[INDEXCOUNT:]
		return src, indexOptionPool, nil
	}
	src = src[INDEXCOUNT:]
	indexN := int(encoding.UnmarshalUint32(src[:4]))
	src = src[4:]
	start := len(indexOptionPool)

	if len(indexOptionPool)+indexN > cap(indexOptionPool) {
		indexOptionPool = append(indexOptionPool[:cap(indexOptionPool)], make([]IndexOption, start+indexN-cap(indexOptionPool))...)
	}
	indexOptionPool = indexOptionPool[:start+indexN]

	for i := 0; i < indexN; i++ {
		if len(src) < 1 {
			return nil, indexOptionPool, errors.New("too small for indexOption key length")
		}

		indexOpt := &indexOptionPool[start+i]

		indexOpt.Oid = encoding.UnmarshalUint32(src[:4])
		src = src[4:]
		indexListLen := encoding.UnmarshalUint16(src[:2])
		if int(indexListLen) < cap(indexOpt.IndexList) {
			indexOpt.IndexList = indexOpt.IndexList[:indexListLen]
		} else {
			indexOpt.IndexList = append(indexOpt.IndexList, make([]uint16, int(indexListLen)-cap(indexOpt.IndexList))...)
		}
		src = src[2:]
		for j := 0; j < int(indexListLen); j++ {
			indexOpt.IndexList[j] = encoding.UnmarshalUint16(src[:2])
			src = src[2:]
		}
	}
	r.IndexOptions = indexOptionPool[start:]
	return src, indexOptionPool, nil
}

func (r *Row) UnmarshalIndexKeys(indexkeypool []byte) []byte {
	indexKl := 4 + // total length of indexkey
		2 + // measurment name length
		len(r.Name) + // measurment name with version
		2 + // tag count
		4*len(r.Tags) + // length of each tag key and value
		r.Tags.TagsSize() // size of tag keys/values
	start := len(indexkeypool)
	if start+indexKl > cap(indexkeypool) {
		indexkeypool = append(indexkeypool[:cap(indexkeypool)], make([]byte, start+indexKl-cap(indexkeypool))...)
	}
	indexkeypool = indexkeypool[:start+indexKl]
	MakeIndexKey(r.Name, r.Tags, indexkeypool[start:start])
	r.IndexKey = indexkeypool[start:]
	return indexkeypool
}

func MakeIndexKey(name string, tags PointTags, dst []byte) []byte {
	indexKl := 4 + // total length of indexkey
		2 + // measurment name length
		len(name) + // measurment name with version
		2 + // tag count
		4*len(tags) + // length of each tag key and value
		tags.TagsSize() // size of tag keys/values
	start := len(dst)

	// marshal total len
	dst = encoding.MarshalUint32(dst, uint32(indexKl))
	// marshal measurement
	dst = encoding.MarshalUint16(dst, uint16(len(name)))
	dst = append(dst, name...)
	// marshal tags
	dst = encoding.MarshalUint16(dst, uint16(len(tags)))
	for i := range tags {
		kl := len(tags[i].Key)
		dst = encoding.MarshalUint16(dst, uint16(kl))
		dst = append(dst, tags[i].Key...)
		vl := len(tags[i].Value)
		dst = encoding.MarshalUint16(dst, uint16(vl))
		dst = append(dst, tags[i].Value...)
	}
	return dst[start:]
}
