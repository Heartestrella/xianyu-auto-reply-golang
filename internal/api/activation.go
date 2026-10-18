package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const activationSalt = "XianyuAutoReply@2024#Lic"

var machineIDPattern = regexp.MustCompile(`^[0-9A-F]{32}$`)
var beijingTZ = time.FixedZone("CST", 8*3600)

func activationMachine(r *http.Request) (string, bool) {
	var p struct {
		MachineID string `json:"machine_id"`
	}
	if decode(r, &p) != nil {
		return "", false
	}
	p.MachineID = strings.ToUpper(strings.TrimSpace(p.MachineID))
	return p.MachineID, machineIDPattern.MatchString(p.MachineID)
}
func activationSign(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return strings.ToUpper(hex.EncodeToString(sum[:]))[:16]
}
func clientIP(r *http.Request) string {
	if x := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); x != "" {
		return x
	}
	if x := strings.TrimSpace(r.Header.Get("X-Real-IP")); x != "" {
		return x
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}
func (s *Server) saveActivation(r *http.Request, machine, typ, code string, days int) error {
	_, e := s.db.ExecContext(r.Context(), `INSERT INTO xy_activation_logs(machine_id,code_type,generated_code,days,ip_address,created_at) VALUES(?,?,?,?,?,?)`, machine, typ, code, days, clientIP(r), time.Now().In(beijingTZ).Format("2006-01-02 15:04:05"))
	return e
}
func (s *Server) activationGenerate(w http.ResponseWriter, r *http.Request) {
	machine, valid := activationMachine(r)
	if !valid {
		write(w, 422, response{Success: false, Message: "机器码应为32位十六进制字符串（0-9, A-F）"})
		return
	}
	var exists int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_activation_logs WHERE machine_id=? AND code_type='generate'`, machine).Scan(&exists)
	if exists > 0 {
		write(w, 200, response{Success: false, Message: "该机器码已获取过试用激活码，每个机器码只能获取一次"})
		return
	}
	expires := time.Now().Add(15 * 24 * time.Hour).Unix()
	code := strings.ToUpper(strconv.FormatInt(expires, 16)) + "-" + activationSign(fmt.Sprintf("%s:%d:%s", machine, expires, activationSalt))
	if e := s.saveActivation(r, machine, "generate", code, 15); e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "激活码生成成功", map[string]any{"activation_code": code, "expire_time": time.Unix(expires, 0).In(beijingTZ).Format("2006-01-02 15:04:05"), "days": 15, "machine_id": machine})
}
func (s *Server) activationRenew(w http.ResponseWriter, r *http.Request) {
	machine, valid := activationMachine(r)
	if !valid {
		write(w, 422, response{Success: false, Message: "机器码应为32位十六进制字符串（0-9, A-F）"})
		return
	}
	var created string
	e := s.db.QueryRowContext(r.Context(), `SELECT created_at FROM xy_activation_logs WHERE machine_id=? AND code_type='renew' ORDER BY created_at DESC LIMIT 1`, machine).Scan(&created)
	if e == nil {
		last, _ := time.ParseInLocation("2006-01-02 15:04:05", created, beijingTZ)
		remaining := 24*time.Hour - time.Since(last)
		if remaining > 0 {
			write(w, 200, response{Success: false, Message: fmt.Sprintf("该机器码每天只能获取续期码一次，请%d小时%d分钟后再试", int(remaining.Hours()), int(remaining.Minutes())%60)})
			return
		}
	}
	markerBytes := make([]byte, 8)
	_, _ = rand.Read(markerBytes)
	marker := strings.ToUpper(hex.EncodeToString(markerBytes))
	duration := 5 * 86400
	durHex := strings.ToUpper(strconv.FormatInt(int64(duration), 16))
	code := "R" + durHex + "-" + marker + "-" + activationSign(fmt.Sprintf("%s:R:%d:%s:%s", machine, duration, marker, activationSalt))
	if e = s.saveActivation(r, machine, "renew", code, 5); e != nil {
		fail(w, 500, e.Error())
		return
	}
	ok(w, "续期码生成成功", map[string]any{"renew_code": code, "days": 5, "machine_id": machine})
}
func (s *Server) activationHistory(w http.ResponseWriter, r *http.Request) {
	machine, valid := activationMachine(r)
	if !valid {
		write(w, 422, response{Success: false, Message: "机器码应为32位十六进制字符串（0-9, A-F）"})
		return
	}
	p, size := pageParams(r)
	var total int
	_ = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM xy_activation_logs WHERE machine_id=?`, machine).Scan(&total)
	rows, e := s.db.QueryContext(r.Context(), `SELECT id,code_type,generated_code,days,COALESCE(ip_address,''),created_at FROM xy_activation_logs WHERE machine_id=? ORDER BY created_at DESC LIMIT ? OFFSET ?`, machine, size, (p-1)*size)
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	defer rows.Close()
	records := []map[string]any{}
	now := time.Now().Unix()
	for rows.Next() {
		var id int64
		var typ, code, ip, created string
		var days int
		_ = rows.Scan(&id, &typ, &code, &days, &ip, &created)
		var expire any
		var expired any
		expireStatus := "不适用"
		if typ == "generate" {
			parts := strings.Split(code, "-")
			if len(parts) == 2 {
				if ts, err := strconv.ParseInt(parts[0], 16, 64); err == nil {
					expire = time.Unix(ts, 0).In(beijingTZ).Format("2006-01-02 15:04:05")
					isExpired := now > ts
					expired = isExpired
					if isExpired {
						expireStatus = "已到期"
					} else {
						expireStatus = "未到期"
					}
				} else {
					expireStatus = "未知"
				}
			} else {
				expireStatus = "未知"
			}
		}
		name := "续期码"
		if typ == "generate" {
			name = "激活码"
		}
		records = append(records, map[string]any{"id": id, "code_type": typ, "code_type_name": name, "generated_code": code, "days": days, "ip_address": ip, "expire_time": expire, "expired": expired, "expire_status": expireStatus, "created_at": created})
	}
	ok(w, "查询成功", map[string]any{"records": records, "total": total, "page": p, "page_size": size, "total_pages": pages(total, size)})
}
