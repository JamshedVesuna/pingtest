// Code generated by protoc-gen-go.
// source: ptprotos/ptprotos.proto
// DO NOT EDIT!

/*
Package ptprotos is a generated protocol buffer package.

Run `protoc --go_out=. ptprotos/*.proto` to generate *.pb.go file.
See instructions at https://github.com/golang/protobuf

It is generated from these files:
	ptprotos/ptprotos.proto

It has these top-level messages:
	PingtestParams
	PingtestMessage
*/
package ptprotos

import proto "github.com/golang/protobuf/proto"
import fmt "fmt"
import math "math"

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// The direction of the pingtest.
type PingtestMessage_Type int32

const (
	PingtestMessage_UNKNOWN  PingtestMessage_Type = 0
	PingtestMessage_UPLOAD   PingtestMessage_Type = 1
	PingtestMessage_DOWNLOAD PingtestMessage_Type = 2
)

var PingtestMessage_Type_name = map[int32]string{
	0: "UNKNOWN",
	1: "UPLOAD",
	2: "DOWNLOAD",
}
var PingtestMessage_Type_value = map[string]int32{
	"UNKNOWN":  0,
	"UPLOAD":   1,
	"DOWNLOAD": 2,
}

func (x PingtestMessage_Type) Enum() *PingtestMessage_Type {
	p := new(PingtestMessage_Type)
	*p = x
	return p
}
func (x PingtestMessage_Type) String() string {
	return proto.EnumName(PingtestMessage_Type_name, int32(x))
}
func (x *PingtestMessage_Type) UnmarshalJSON(data []byte) error {
	value, err := proto.UnmarshalJSONEnum(PingtestMessage_Type_value, data, "PingtestMessage_Type")
	if err != nil {
		return err
	}
	*x = PingtestMessage_Type(value)
	return nil
}

// Parameters for the Pingtest algorithm sent from the client to the server.
// The client sends UDP packets of varying size to the server. The server sends
// a tiny TCP ACK back to the client. The client then updates its sending packet
// size accordingly.
type PingtestParams struct {
	// The index of the current packet that the client sends.
	// This field is 0 indexed.
	PacketIndex *int64 `protobuf:"varint,1,opt,name=packet_index" json:"packet_index,omitempty"`
	// This field holds the client's timestamp of when the client sent this
	// packet, in the number of nanoseconds elapsed since January 1, 1970 UTC.
	ClientTimestampNano *int64 `protobuf:"varint,2,opt,name=client_timestamp_nano" json:"client_timestamp_nano,omitempty"`
	// This field holds the server's timestamp of when the server received this
	// packet, in the number of nanoseconds elapsed since January 1, 1970 UTC.
	ServerTimestampNano *int64 `protobuf:"varint,3,opt,name=server_timestamp_nano" json:"server_timestamp_nano,omitempty"`
	// The size of the packet, in bytes, that the client sends the server. This
	// includes padding.
	PacketSizeBytes  *int64 `protobuf:"varint,4,opt,name=packet_size_bytes" json:"packet_size_bytes,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *PingtestParams) Reset()         { *m = PingtestParams{} }
func (m *PingtestParams) String() string { return proto.CompactTextString(m) }
func (*PingtestParams) ProtoMessage()    {}

func (m *PingtestParams) GetPacketIndex() int64 {
	if m != nil && m.PacketIndex != nil {
		return *m.PacketIndex
	}
	return 0
}

func (m *PingtestParams) GetClientTimestampNano() int64 {
	if m != nil && m.ClientTimestampNano != nil {
		return *m.ClientTimestampNano
	}
	return 0
}

func (m *PingtestParams) GetServerTimestampNano() int64 {
	if m != nil && m.ServerTimestampNano != nil {
		return *m.ServerTimestampNano
	}
	return 0
}

func (m *PingtestParams) GetPacketSizeBytes() int64 {
	if m != nil && m.PacketSizeBytes != nil {
		return *m.PacketSizeBytes
	}
	return 0
}

// The message that is sent on the wire from the client to the server.
type PingtestMessage struct {
	// Parameters for the pingtest algorithm.
	PingtestParams *PingtestParams `protobuf:"bytes,1,opt,name=pingtest_params" json:"pingtest_params,omitempty"`
	// Extra bytes to pad the payload to a specific size.
	// Bytes are randomized to mitigate caching.
	Padding          []byte                `protobuf:"bytes,2,opt,name=padding" json:"padding,omitempty"`
	Type             *PingtestMessage_Type `protobuf:"varint,3,opt,name=type,enum=ptprotos.PingtestMessage_Type" json:"type,omitempty"`
	XXX_unrecognized []byte                `json:"-"`
}

func (m *PingtestMessage) Reset()         { *m = PingtestMessage{} }
func (m *PingtestMessage) String() string { return proto.CompactTextString(m) }
func (*PingtestMessage) ProtoMessage()    {}

func (m *PingtestMessage) GetPingtestParams() *PingtestParams {
	if m != nil {
		return m.PingtestParams
	}
	return nil
}

func (m *PingtestMessage) GetPadding() []byte {
	if m != nil {
		return m.Padding
	}
	return nil
}

func (m *PingtestMessage) GetType() PingtestMessage_Type {
	if m != nil && m.Type != nil {
		return *m.Type
	}
	return PingtestMessage_UNKNOWN
}

func init() {
	proto.RegisterEnum("ptprotos.PingtestMessage_Type", PingtestMessage_Type_name, PingtestMessage_Type_value)
}
