package xianyu

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseIncomingManyExtractsOrderIDFromSyncPushPackage(t *testing.T) {
	const orderID = "1234567890123456789"
	decoded := map[string]any{
		"1": map[string]any{
			"3": "chat-id",
			"6": map[string]any{
				"3": map[string]any{
					"2": "[我已付款，等待你发货]",
					"5": `{"dxCard":{"item":{"main":{"exContent":{"button":{"targetUrl":"fleamarket://order_detail?id=` + orderID + `&role=Seller"}}}}}}`,
				},
			},
			"10": map[string]any{
				"reminderContent": "[我已付款，等待你发货]",
				"senderUserId":    "buyer-id",
				"reminderUrl":     "fleamarket://message_chat?itemId=item-id&peerUserId=buyer-id&sid=chat-id",
			},
		},
	}
	encoded := base64.StdEncoding.EncodeToString(encodeTestMessagePackMap(decoded))
	frame, err := json.Marshal(map[string]any{
		"headers": map[string]any{"lwp": "/s/sync"},
		"body": map[string]any{"syncPushPackage": map[string]any{
			"data": []any{map[string]any{"bizType": 40, "data": encoded}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	incoming := ParseIncomingMany("account-id", frame)
	if len(incoming) != 1 {
		t.Fatalf("got %d incoming messages, want 1", len(incoming))
	}
	if got := extractOrderID(incoming[0].Raw); got != orderID {
		t.Fatalf("extractOrderID() = %q, want %q", got, orderID)
	}
}

func encodeTestMessagePackMap(value map[string]any) []byte {
	encoded, _ := json.Marshal(value)
	var decoded any
	_ = json.Unmarshal(encoded, &decoded)
	return encodeTestMessagePackValue(decoded)
}

func encodeTestMessagePackValue(value any) []byte {
	switch value := value.(type) {
	case map[string]any:
		out := []byte{byte(0x80 | len(value))}
		for key, item := range value {
			out = append(out, encodeTestMessagePackValue(key)...)
			out = append(out, encodeTestMessagePackValue(item)...)
		}
		return out
	case string:
		if len(value) < 32 {
			return append([]byte{byte(0xa0 | len(value))}, []byte(value)...)
		}
		if len(value) < 256 {
			return append([]byte{0xd9, byte(len(value))}, []byte(value)...)
		}
		panic("test string is too long for str8")
	default:
		panic("unsupported test MessagePack value")
	}
}
