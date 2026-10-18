package xianyu

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

const AppKey = "444e9908a51d1cb236a27862abc769c9"

func ParseCookies(raw string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		p := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(p) == 2 && p[0] != "" {
			m[p[0]] = p[1]
		}
	}
	return m
}
func Sign(timestamp, token, data string) string {
	sum := md5.Sum([]byte(token + "&" + timestamp + "&34839810&" + data))
	return hex.EncodeToString(sum[:])
}
func Mid() string  { return fmt.Sprintf("%d%d 0", rand.IntN(1000), time.Now().UnixMilli()) }
func UUID() string { return fmt.Sprintf("-%d1", time.Now().UnixMilli()) }
func DeviceID(userID string) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 36)
	for i := range b {
		switch i {
		case 8, 13, 18, 23:
			b[i] = '-'
		case 14:
			b[i] = '4'
		case 19:
			b[i] = chars[(rand.IntN(16)&3)|8]
		default:
			b[i] = chars[rand.IntN(16)]
		}
	}
	return string(b) + "-" + userID
}
func Register(token, device string) []byte {
	v := map[string]any{"lwp": "/reg", "headers": map[string]any{"cache-header": "app-key token ua wv", "app-key": AppKey, "token": token, "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36", "dt": "j", "wv": "im:3,au:3,sy:6", "sync": "0,0;0;0;", "did": device, "mid": Mid()}}
	b, _ := json.Marshal(v)
	return b
}

func CreateChat(toUserID, myID, itemID string) ([]byte, string) {
	mid := Mid()
	v := map[string]any{"lwp": "/r/SingleChatConversation/create", "headers": map[string]string{"mid": mid}, "body": []any{map[string]any{"pairFirst": strings.TrimSuffix(toUserID, "@goofish") + "@goofish", "pairSecond": strings.TrimSuffix(myID, "@goofish") + "@goofish", "bizType": "1", "extension": map[string]string{"itemId": itemID}, "ctx": map[string]string{"appVersion": "1.0", "platform": "web"}}}}
	b, _ := json.Marshal(v)
	return b, mid
}
func Sync() []byte {
	now := time.Now().UnixMilli()
	v := map[string]any{"lwp": "/r/SyncStatus/ackDiff", "headers": map[string]any{"mid": Mid()}, "body": []any{map[string]any{"pipeline": "sync", "tooLong2Tag": "PNM,1", "channel": "sync", "topic": "sync", "highPts": 0, "pts": now * 1000, "seq": 0, "timestamp": now}}}
	b, _ := json.Marshal(v)
	return b
}
func TextMessage(chatID, toUserID, myID, text string) ([]byte, string) {
	content, _ := json.Marshal(map[string]any{"contentType": 1, "text": map[string]string{"text": text}})
	mid := Mid()
	v := map[string]any{"lwp": "/r/MessageSend/sendByReceiverScope", "headers": map[string]string{"mid": mid}, "body": []any{map[string]any{"uuid": UUID(), "cid": chatID + "@goofish", "conversationType": 1, "content": map[string]any{"contentType": 101, "custom": map[string]any{"type": 1, "data": base64.StdEncoding.EncodeToString(content)}}, "redPointPolicy": 0, "extension": map[string]string{"extJson": "{}"}, "ctx": map[string]string{"appVersion": "1.0", "platform": "web"}, "mtags": map[string]any{}, "msgReadStatusSetting": 1}, map[string]any{"actualReceivers": []string{toUserID + "@goofish", myID + "@goofish"}}}}
	b, _ := json.Marshal(v)
	return b, mid
}
func RecallMessage(messageID string) []byte {
	mid := Mid()
	v := map[string]any{"lwp": "/r/MessageManager/recallMessage", "headers": map[string]string{"mid": mid}, "body": []string{messageID}}
	b, _ := json.Marshal(v)
	return b
}
func ImageMessage(chatID, toUserID, myID, imageURL string, width, height int) ([]byte, string) {
	if width < 1 {
		width = 800
	}
	if height < 1 {
		height = 600
	}
	content, _ := json.Marshal(map[string]any{"contentType": 2, "image": map[string]any{"pics": []map[string]any{{"height": height, "type": 0, "url": imageURL, "width": width}}}})
	mid := Mid()
	v := map[string]any{"lwp": "/r/MessageSend/sendByReceiverScope", "headers": map[string]string{"mid": mid}, "body": []any{map[string]any{"uuid": UUID(), "cid": strings.TrimSuffix(chatID, "@goofish") + "@goofish", "conversationType": 1, "content": map[string]any{"contentType": 101, "custom": map[string]any{"type": 1, "data": base64.StdEncoding.EncodeToString(content)}}, "redPointPolicy": 0, "extension": map[string]string{"extJson": "{}"}, "ctx": map[string]string{"appVersion": "1.0", "platform": "web"}, "mtags": map[string]any{}, "msgReadStatusSetting": 1}, map[string]any{"actualReceivers": []string{strings.TrimSuffix(toUserID, "@goofish") + "@goofish", strings.TrimSuffix(myID, "@goofish") + "@goofish"}}}}
	b, _ := json.Marshal(v)
	return b, mid
}
