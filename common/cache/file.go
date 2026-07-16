package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type FileProvider struct {
	path string
}

type FileItem struct {
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
	NoExpiry  bool      `json:"no_expiry"`
}

func NewFileProvider(path string) *FileProvider {
	os.MkdirAll(path, 0755)

	return &FileProvider{
		path: path,
	}
}

func (f *FileProvider) Set(ctx context.Context, key string, value any, exp time.Duration) error {
	item := FileItem{
		Value:     value.(string),
		ExpiresAt: time.Now().Add(exp),
	}

	fmt.Printf("DEBUG: exp=%v\n", exp)

	if exp == 0 {
		item.NoExpiry = true
	} else {
		item.ExpiresAt = time.Now().Add(exp)
	}
	data, _ := json.Marshal(item)

	return os.WriteFile(
		filepath.Join(f.path, key+".json"),
		data,
		0644,
	)
}

func (f *FileProvider) SetNX(ctx context.Context, key string, value any, exp time.Duration) (bool, error) {
	item := FileItem{
		Value: value.(string),
	}
	if exp == 0 {
		item.NoExpiry = true
	} else {
		item.ExpiresAt = time.Now().Add(exp)
	}
	data, err := json.Marshal(item)
	if err != nil {
		return false, err
	}

	file, err := os.OpenFile(filepath.Join(f.path, key+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return false, err
	}
	return true, nil
}

func (f *FileProvider) Get(ctx context.Context, key string) (string, error) {
	data, err := os.ReadFile(filepath.Join(f.path, key+".json"))
	if err != nil {
		return "", err
	}

	var item FileItem

	if err := json.Unmarshal(data, &item); err != nil {
		return "", err
	}

	if !item.NoExpiry && time.Now().After(item.ExpiresAt) {
		_ = os.Remove(filepath.Join(f.path, key+".json"))
		return "", os.ErrNotExist
	}

	return item.Value, nil
}

func (f *FileProvider) Delete(ctx context.Context, key string) error {
	return os.Remove(filepath.Join(f.path, key+".json"))
}

func (f *FileProvider) Ping(ctx context.Context) error {
	return nil
}

func (f *FileProvider) Close() error {
	return nil
}
