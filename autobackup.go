package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Scheduled backups: the enforce job seals a backup with the plan's passphrase and sends it off
// the server, to the alert Telegram chat and/or an S3-compatible bucket (Cloudflare R2, AWS S3,
// MinIO, ...). A seized or blocked server can then be rebuilt with `xtune migrate --backup FILE`
// even when nobody ran `xtune backup` in time. The passphrase is on the server too (the server
// holds every secret anyway); it protects the copies elsewhere, so the admin keeps it apart.

// BackupPlan is the schedule and where the backups go.
type BackupPlan struct {
	Every    int64     `json:"every"`              // seconds between backups
	Pass     string    `json:"pass"`               // passphrase the backups are sealed with
	Telegram bool      `json:"telegram,omitempty"` // send to the alert chat (xtune notify telegram)
	S3       *S3Target `json:"s3,omitempty"`
}

// S3Target is a bucket on an S3-compatible service, addressed path-style.
type S3Target struct {
	Endpoint  string `json:"endpoint"` // https://ACCOUNT.r2.cloudflarestorage.com
	Region    string `json:"region"`   // "auto" for R2
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// backupRetry is how soon a failed scheduled backup is tried again.
const backupRetry = time.Hour

func (b *BackupPlan) describe() string {
	if b == nil {
		return "off"
	}
	var to []string
	if b.Telegram {
		to = append(to, "the alert Telegram chat")
	}
	if b.S3 != nil {
		to = append(to, fmt.Sprintf("s3 %s/%s/%s", b.S3.Endpoint, b.S3.Bucket, b.S3.Prefix))
	}
	return fmt.Sprintf("every %s, sealed, to %s", fmtSince(time.Duration(b.Every)*time.Second), strings.Join(to, " and "))
}

// backupDue reports whether the scheduled backup should run now: its period is over, or the last
// attempt failed and an hour has passed.
func backupDue(st *State, us *Usage, now time.Time) bool {
	b := st.Backups
	if b == nil || b.Every <= 0 || st.Follow != nil { // a follower's users and keys come from its primary
		return false
	}
	since := now.Unix() - us.BackupAt
	if us.BackupAt > us.LastBackup && since >= int64(backupRetry/time.Second) {
		return true
	}
	return since >= b.Every
}

// runScheduledBackup sends a sealed backup to every target and records how it went.
func runScheduledBackup(st *State, us *Usage, now time.Time) error {
	us.BackupAt = now.Unix()
	err := sendBackup(st, &Backup{Version: version, Created: now.Unix(), State: st, Usage: us}, now)
	if err != nil {
		us.BackupErr = tail(err.Error(), 300)
		return err
	}
	us.LastBackup, us.BackupErr = now.Unix(), ""
	return nil
}

// sendBackup seals b and delivers it to the plan's targets.
func sendBackup(st *State, b *Backup, now time.Time) error {
	p := st.Backups
	if p == nil || !p.Telegram && p.S3 == nil {
		return errors.New("no backup target set")
	}
	data, err := backupBytes(b, p.Pass)
	if err != nil {
		return err
	}
	name := backupName(st, now)
	var errs []string
	if p.Telegram {
		caption := fmt.Sprintf("xtune backup of %s, %d users. Sealed: restore with xtune migrate --backup FILE --to NEWHOST and the backup passphrase.",
			hostsOf(st), len(st.Users))
		if err := telegramSendFile(st.Notify, name, data, caption); err != nil {
			errs = append(errs, "telegram: "+err.Error())
		}
	}
	if p.S3 != nil {
		if err := s3Put(*p.S3, p.S3.key(name), data, now); err != nil {
			errs = append(errs, "s3: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// telegramSendFile sends a document to the alert chat.
func telegramSendFile(n Notify, name string, data []byte, caption string) error {
	if n.TelegramToken == "" || n.TelegramChat == "" {
		return errors.New("Telegram alerts are not set up (xtune notify telegram --chat ID)")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("chat_id", n.TelegramChat)
	mw.WriteField("caption", caption)
	fw, _ := mw.CreateFormFile("document", name)
	fw.Write(data)
	mw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", telegramAPI+"/bot"+n.TelegramToken+"/sendDocument", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("User-Agent", "xtune/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), n.TelegramToken, "***"))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ---------------------------------------------------------------- S3

func (t S3Target) key(name string) string {
	return strings.Trim(strings.Trim(t.Prefix, "/")+"/"+name, "/")
}

// parseS3 reads https://ENDPOINT/BUCKET[/PREFIX].
func parseS3(raw, region, access, secret string) (*S3Target, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%q: want https://ENDPOINT/BUCKET[/PREFIX]", raw)
	}
	bucket, prefix, _ := strings.Cut(strings.Trim(u.Path, "/"), "/")
	if bucket == "" {
		return nil, fmt.Errorf("%q has no bucket: want https://ENDPOINT/BUCKET[/PREFIX]", raw)
	}
	if access == "" || secret == "" {
		return nil, errors.New("S3 needs an access key and a secret key (XTUNE_S3_ACCESS_KEY, XTUNE_S3_SECRET_KEY)")
	}
	return &S3Target{Endpoint: "https://" + u.Host, Region: orStr(region, "auto"), Bucket: bucket, Prefix: prefix,
		AccessKey: access, SecretKey: secret}, nil
}

// s3Escape encodes a path the way SigV4 wants: every byte but unreserved ones, '/' kept.
func s3Escape(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || strings.IndexByte("-_.~/", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// sigV4 is the AWS Signature Version 4 Authorization header for a request whose headers (lower
// case names, including host, x-amz-date and x-amz-content-sha256) are all signed.
func sigV4(method, path, query string, headers map[string]string, payloadHash, region, service, access, secret string, now time.Time) string {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	creq := strings.Join([]string{method, path, query, canon.String(), signed, payloadHash}, "\n")
	date := now.UTC().Format("20060102")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + now.UTC().Format("20060102T150405Z") + "\n" + scope + "\n" + sha256Hex([]byte(creq))
	k := hmacSHA([]byte("AWS4"+secret), date)
	k = hmacSHA(k, region)
	k = hmacSHA(k, service)
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, sts))
	return "AWS4-HMAC-SHA256 Credential=" + access + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig
}

// s3Put uploads body to key in the bucket.
func s3Put(t S3Target, key string, body []byte, now time.Time) error {
	u, err := url.Parse(t.Endpoint)
	if err != nil {
		return err
	}
	path := s3Escape("/" + t.Bucket + "/" + key)
	hash := sha256Hex(body)
	amz := now.UTC().Format("20060102T150405Z")
	h := map[string]string{"host": u.Host, "x-amz-content-sha256": hash, "x-amz-date": amz}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "PUT", t.Endpoint+path, bytes.NewReader(body))
	req.Header.Set("x-amz-content-sha256", hash)
	req.Header.Set("x-amz-date", amz)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", sigV4("PUT", path, "", h, hash, t.Region, "s3", t.AccessKey, t.SecretKey, now))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ---------------------------------------------------------------- settings

// SetBackupPlan replaces the schedule (nil turns it off) and, when on, sends a first backup now so
// a wrong token or key shows at once.
func SetBackupPlan(r *Server, p *BackupPlan) (*State, error) {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return nil, err
	}
	if st.Follow != nil && p != nil {
		return nil, errors.New("this server follows a primary, which holds the users: schedule backups there")
	}
	if p != nil && p.Telegram && (st.Notify.TelegramToken == "" || st.Notify.TelegramChat == "") {
		return nil, errors.New("backups to Telegram go to the alert chat: set it up first (xtune notify telegram --chat ID)")
	}
	st.Backups = p
	if err := saveState(r, st); err != nil {
		return nil, err
	}
	if p == nil {
		return st, nil
	}
	return st, backupNowLocked(r, st)
}

// BackupNow sends a scheduled-style backup at once.
func BackupNow(r *Server) error {
	defer r.lock()()
	st, err := requireState(r)
	if err != nil {
		return err
	}
	if st.Backups == nil {
		return errors.New("no backup schedule: xtune backup schedule --telegram | --s3 URL")
	}
	return backupNowLocked(r, st)
}

func backupNowLocked(r *Server, st *State) error {
	us, err := LoadUsage(r)
	if err != nil {
		return err
	}
	err = runScheduledBackup(st, us, time.Now())
	if serr := saveUsage(r, us); err == nil {
		err = serr
	}
	return err
}

// cmdBackupSchedule is `xtune backup schedule [status | off | now] [--every 24h] [--telegram] [--s3 URL]`.
func cmdBackupSchedule(args []string) error {
	if err := serverOnly("scheduled backups", args); err != nil {
		return err
	}
	fs := flag.NewFlagSet("backup schedule", flag.ExitOnError)
	every := fs.Duration("every", 24*time.Hour, "time between backups")
	tg := fs.Bool("telegram", false, "send them to the alert Telegram chat (xtune notify telegram)")
	s3 := fs.String("s3", "", "S3-compatible bucket: https://ENDPOINT/BUCKET[/PREFIX] (keys: XTUNE_S3_ACCESS_KEY, XTUNE_S3_SECRET_KEY)")
	region := fs.String("s3-region", "auto", "the bucket's region (auto for Cloudflare R2)")
	pos := parse(fs, args)
	r, err := OpenLocal()
	if err != nil {
		return err
	}
	action := ""
	if len(pos) > 0 {
		action = pos[0]
	}
	set := flagsSet(fs)
	if action == "" && !set["telegram"] && !set["s3"] && !set["every"] {
		action = "status"
	}
	switch action {
	case "status":
		st, err := requireState(r)
		if err != nil {
			return err
		}
		us, err := LoadUsage(r)
		if err != nil {
			return err
		}
		fmt.Println("scheduled backups:", st.Backups.describe())
		if us.LastBackup > 0 {
			fmt.Println("last delivered:   ", time.Unix(us.LastBackup, 0).Format("2006-01-02 15:04"))
		}
		if us.BackupErr != "" && us.BackupAt > us.LastBackup {
			fmt.Println("last attempt failed:", us.BackupErr)
		}
		return nil
	case "off":
		_, err := SetBackupPlan(r, nil)
		if err == nil {
			fmt.Println("scheduled backups: off")
		}
		return err
	case "now":
		if err := BackupNow(r); err != nil {
			return err
		}
		fmt.Println("[+] backup sent")
		return nil
	case "":
	default:
		return fmt.Errorf("unknown backup schedule action %q (status, off, now)", action)
	}
	st, err := requireState(r)
	if err != nil {
		return err
	}
	p := &BackupPlan{Every: int64(every.Seconds()), Telegram: *tg}
	if old := st.Backups; old != nil { // changing the period or a target keeps the passphrase
		p.Pass = old.Pass
		if !set["telegram"] {
			p.Telegram = old.Telegram
		}
		if !set["s3"] {
			p.S3 = old.S3
		}
	}
	if p.Every < 3600 {
		return errors.New("--every: at least 1h")
	}
	if *s3 != "" {
		ak, sk := os.Getenv("XTUNE_S3_ACCESS_KEY"), os.Getenv("XTUNE_S3_SECRET_KEY")
		if ak == "" {
			if ak, err = cliPrompt("S3 access key ID", false); err != nil {
				return err
			}
		}
		if sk == "" {
			if sk, err = cliPrompt("S3 secret access key", true); err != nil {
				return err
			}
		}
		if p.S3, err = parseS3(*s3, *region, strings.TrimSpace(ak), strings.TrimSpace(sk)); err != nil {
			return err
		}
	}
	if !p.Telegram && p.S3 == nil {
		return errors.New("backup schedule: pass --telegram and/or --s3 URL")
	}
	newPass := p.Pass == ""
	if newPass {
		p.Pass = os.Getenv("XTUNE_BACKUP_PASSWORD")
		if p.Pass == "" {
			p.Pass = randHex(12)
		} else if len(p.Pass) < 8 {
			return errors.New("XTUNE_BACKUP_PASSWORD is too short (at least 8 characters)")
		}
	}
	if _, err := SetBackupPlan(r, p); err != nil {
		return fmt.Errorf("the schedule is saved, but the first backup failed (retried every hour): %w", err)
	}
	fmt.Println("scheduled backups:", p.describe())
	fmt.Println("[+] the first backup was sent")
	if newPass {
		fmt.Printf("\nBackup passphrase: %s\nWrite it down somewhere other than this server: restoring needs it\n"+
			"(XTUNE_BACKUP_PASSWORD=... xtune migrate --backup FILE --to NEWHOST).\n", p.Pass)
	}
	return nil
}
