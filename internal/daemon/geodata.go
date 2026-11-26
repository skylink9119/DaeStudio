package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const geoMaxSize = 64 << 20

type GeoRelease struct {
	Kind        string `json:"kind"`
	Latest      string `json:"latest"`
	URL         string `json:"url"`
	CurrentHash string `json:"currentHash"`
	Digest      string `json:"digest"`
	Available   bool   `json:"available"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	download    string
}

func geoSource(kind string) (string, string, error) {
	switch kind {
	case "geosite":
		return "v2fly/domain-list-community", "dlc.dat", nil
	case "geoip":
		return "v2fly/geoip", "geoip.dat", nil
	default:
		return "", "", fmt.Errorf("未知数据库类型")
	}
}

func geoPath(kind string) string { return filepath.Join("/usr/share/dae", kind+".dat") }
func geoHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (m *Manager) CheckGeo(ctx context.Context, kind string) (GeoRelease, error) {
	repo, name, err := geoSource(kind)
	if err != nil {
		return GeoRelease{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body, err := fetchCore(ctx, "https://api.github.com/repos/"+repo+"/releases/latest")
	if err != nil {
		return GeoRelease{}, err
	}
	defer body.Close()
	var release struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err = json.NewDecoder(io.LimitReader(body, 2<<20)).Decode(&release); err != nil {
		return GeoRelease{}, err
	}
	r := GeoRelease{Kind: kind, Latest: release.Tag, URL: "https://github.com/" + repo + "/releases/tag/" + url.PathEscape(release.Tag)}
	for _, a := range release.Assets {
		if a.Name == name && strings.HasPrefix(a.Digest, "sha256:") {
			r.Digest = strings.TrimPrefix(a.Digest, "sha256:")
		}
	}
	hash, err := hex.DecodeString(r.Digest)
	if err != nil || len(hash) != sha256.Size || release.Tag == "" {
		return r, fmt.Errorf("官方发行缺少有效 SHA-256 摘要，停止更新")
	}
	r.download = "https://github.com/" + repo + "/releases/download/" + url.PathEscape(release.Tag) + "/" + name
	data, err := os.ReadFile(geoPath(kind))
	if err != nil && !os.IsNotExist(err) {
		return r, err
	}
	if err == nil {
		r.CurrentHash = geoHash(data)
		if info, e := os.Stat(geoPath(kind)); e == nil {
			r.UpdatedAt = info.ModTime().Format(time.RFC3339)
		}
	}
	r.Available = r.CurrentHash != r.Digest
	return r, nil
}

// publishGeo stages and syncs on the target filesystem, retaining a rollback copy.
func publishGeo(path string, data []byte, verify func() error) error {
	previous, err := os.ReadFile(path)
	hadPrevious := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if hadPrevious {
		if err := writeGeoFile(path+".previous", previous); err != nil {
			return err
		}
	}
	if err := writeGeoFile(path, data); err != nil {
		return err
	}
	if err := verify(); err != nil {
		var restore error
		if hadPrevious {
			restore = writeGeoFile(path, previous)
		} else {
			restore = os.Remove(path)
		}
		if restore != nil {
			return errors.Join(err, fmt.Errorf("恢复数据库失败: %w", restore))
		}
		return fmt.Errorf("%w；已恢复原数据库", err)
	}
	return nil
}

func writeGeoFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".geodata-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Chmod(0644); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (m *Manager) InstallGeo(ctx context.Context, kind, digest string) error {
	if m.mockMode {
		return fmt.Errorf("当前环境不支持更新真实数据库")
	}
	r, err := m.CheckGeo(ctx, kind)
	if err != nil {
		return err
	}
	if r.Digest != digest {
		return fmt.Errorf("发行版本已变化，请重新检测")
	}
	if !r.Available {
		return nil
	}
	path := geoPath(kind)
	// A config-local database takes precedence in some dae deployments.
	for _, dir := range []string{filepath.Dir(m.configPath), "/usr/local/share/dae"} {
		if _, err := os.Stat(filepath.Join(dir, kind+".dat")); err == nil {
			return fmt.Errorf("发现其他目录的 %s.dat，请先统一数据库路径至 /usr/share/dae", kind)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	body, err := fetchCore(ctx, r.download)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(body, geoMaxSize+1))
	body.Close()
	if readErr != nil {
		return readErr
	}
	if len(data) < 1024 || len(data) > geoMaxSize || geoHash(data) != r.Digest {
		return fmt.Errorf("数据库大小或 SHA-256 校验失败，原文件未改动")
	}
	cfg, err := os.ReadFile(m.configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	pid := detectDaePID()
	err = publishGeo(path, data, func() error {
		if ok, msg := m.Validate(string(cfg)); !ok {
			return fmt.Errorf("新数据库配置校验失败: %s", msg)
		}
		if pid > 0 {
			return m.reloadCurrentConfig(pid, false)
		}
		return nil
	})
	if err != nil {
		m.appendLog("error", "数据库更新失败: "+err.Error()+"；如热重载超时，请检查核心状态后重新应用配置")
		return err
	}
	m.appendLog("info", kind+" 数据库已更新至 "+r.Latest)
	return nil
}
