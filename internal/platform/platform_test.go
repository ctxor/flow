package platform

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/silenceper/wechat/v2/util"
	"protobuf/internal/config"
	"protobuf/internal/model"
	"protobuf/internal/testutil"
)

func encryptedFeishu(t *testing.T, key string, plain []byte) []byte {
	t.Helper()
	digest := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := bytes.Repeat([]byte{42}, 16)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	payload, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(append(iv, out...))})
	return payload
}
func TestFeishuRejectsForgedCallbacks(t *testing.T) {
	c := config.Config{FeishuAppID: "app", FeishuEncryptKey: "encryption-key", FeishuToken: "verification-token"}
	m := New(c, nil, nil)
	plain := []byte(`{"header":{"app_id":"app","token":"verification-token","event_type":"im.message.receive_v1"}}`)
	body := encryptedFeishu(t, c.FeishuEncryptKey, plain)
	req := httptest.NewRequest("POST", "/callbacks/feishu", bytes.NewReader(body))
	w := httptest.NewRecorder()
	m.FeishuWebhook(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
func TestFeishuChallengeAndPersistentDedupe(t *testing.T) {
	h := testutil.New(t)
	c := config.Config{FeishuAppID: "app", FeishuEncryptKey: "encryption-key", FeishuToken: "verification-token"}
	m := New(c, h.Core, h.Redis)
	challenge := encryptedFeishu(t, c.FeishuEncryptKey, []byte(`{"type":"url_verification","token":"verification-token","challenge":"test-challenge"}`))
	w := httptest.NewRecorder()
	m.FeishuWebhook(w, httptest.NewRequest("POST", "/callbacks/feishu", bytes.NewReader(challenge)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "test-challenge") {
		t.Fatalf("challenge: %d %s", w.Code, w.Body.String())
	}
	text, _ := json.Marshal(map[string]string{"text": "创建任务：test"})
	plain, _ := json.Marshal(map[string]any{"header": map[string]string{"app_id": "app", "token": c.FeishuToken, "event_id": "event1", "event_type": "im.message.receive_v1"}, "event": map[string]any{"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "open_alice"}}, "message": map[string]string{"message_id": "message1", "chat_type": "p2p", "message_type": "text", "content": string(text)}}})
	body := encryptedFeishu(t, c.FeishuEncryptKey, plain)
	sum := sha256.Sum256(append([]byte("123nonce"+c.FeishuEncryptKey), body...))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/callbacks/feishu", bytes.NewReader(body))
		req.Header.Set("X-Lark-Request-Timestamp", "123")
		req.Header.Set("X-Lark-Request-Nonce", "nonce")
		req.Header.Set("X-Lark-Signature", hex.EncodeToString(sum[:]))
		w = httptest.NewRecorder()
		m.FeishuWebhook(w, req)
		if w.Code != 200 {
			t.Fatalf("callback: %d %s", w.Code, w.Body.String())
		}
	}
	var count int64
	h.DB.Model(&model.InboxEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate callbacks persisted: %d", count)
	}
}
func TestWechatSignatureAndEncryptedPersistence(t *testing.T) {
	h := testutil.New(t)
	key := strings.TrimRight(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "=")
	c := config.Config{WechatAppID: "wx-app", WechatToken: "wx-token", WechatAESKey: key}
	m := New(c, h.Core, h.Redis)
	bad := httptest.NewRecorder()
	m.WechatWebhook(bad, httptest.NewRequest("POST", "/callbacks/wechat?encrypt_type=aes", strings.NewReader("<xml/>")))
	if bad.Code != 401 {
		t.Fatal(bad.Code)
	}
	raw := []byte(`<xml><ToUserName><![CDATA[service]]></ToUserName><FromUserName><![CDATA[openid]]></FromUserName><CreateTime>1792890000</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[创建任务：test]]></Content><MsgId>123456789</MsgId></xml>`)
	encrypted, err := util.EncryptMsg(bytes.Repeat([]byte{3}, 16), raw, c.WechatAppID, key)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := fmt.Sprint(time.Now().Unix())
	nonce := "testnonce"
	signature := util.Signature(c.WechatToken, timestamp, nonce)
	msgSignature := util.Signature(c.WechatToken, timestamp, nonce, string(encrypted))
	body := `<xml><Encrypt><![CDATA[` + string(encrypted) + `]]></Encrypt></xml>`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/callbacks/wechat?encrypt_type=aes&timestamp="+timestamp+"&nonce="+nonce+"&signature="+signature+"&msg_signature="+msgSignature, strings.NewReader(body))
		w := httptest.NewRecorder()
		m.WechatWebhook(w, req)
		if w.Code != 200 {
			t.Fatalf("WeChat response: %d %s", w.Code, w.Body.String())
		}
		if w.Body.String() != "success" {
			t.Fatalf("expected asynchronous acknowledgement: %q", w.Body.String())
		}
	}
	var count int64
	h.DB.Model(&model.InboxEvent{}).Count(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
