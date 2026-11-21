package storage

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

type StorageProvider interface {
	CreateVolume(ctx context.Context, name string) (string, error)
	DeleteVolume(ctx context.Context, name string) error
}

type Service struct {
	repo        repository.VolumeRepository
	runtime     podman.ContainerRuntime
	storageRoot string
}

func NewService(repo repository.VolumeRepository, runtime podman.ContainerRuntime, storageRoot string) *Service {
	if storageRoot == "" {
		home, _ := os.UserHomeDir()
		storageRoot = filepath.Join(home, ".local", "share", "procloud", "volumes")
	}
	_ = os.MkdirAll(storageRoot, 0755)

	return &Service{
		repo:        repo,
		runtime:     runtime,
		storageRoot: storageRoot,
	}
}

// CreateVolume provisions persistent local volume directory independent of container lifecycle.
func (s *Service) CreateVolume(ctx context.Context, tenantID, projectID, name string, sizeMB int64) (*protocol.Volume, error) {
	if sizeMB <= 0 {
		sizeMB = 1024 // 1GB default
	}

	volPath := filepath.Join(s.storageRoot, tenantID, name)
	if err := os.MkdirAll(volPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create local volume directory: %w", err)
	}

	// Also provision Podman volume for Quadlet compatibility
	podmanVolName := fmt.Sprintf("pcvol-%s-%s", tenantID, name)
	_, _ = s.runtime.CreateVolume(ctx, podmanVolName)

	vol := &protocol.Volume{
		ID:          fmt.Sprintf("vol-%d", time.Now().UnixNano()),
		TenantID:    tenantID,
		ProjectID:   projectID,
		Name:        name,
		Driver:      "local",
		HostPath:    volPath,
		SizeMB:      sizeMB,
		Status:      "available",
		CreatedAt:   time.Now(),
	}

	if err := s.repo.CreateVolume(ctx, vol); err != nil {
		return nil, fmt.Errorf("failed to save volume: %w", err)
	}

	return vol, nil
}

// AttachVolume marks volume as bound to an application and mount path.
func (s *Service) AttachVolume(ctx context.Context, volumeID, appName, mountPath string) error {
	return s.repo.UpdateVolumeAttachment(ctx, volumeID, appName, mountPath, "in-use")
}

// DetachVolume releases volume attachment while preserving all underlying data.
func (s *Service) DetachVolume(ctx context.Context, volumeID string) error {
	return s.repo.UpdateVolumeAttachment(ctx, volumeID, "", "", "available")
}

// BackupVolume creates a compressed .tar.gz snapshot of the volume directory.
func (s *Service) BackupVolume(ctx context.Context, volumeID, destDir string) (string, error) {
	vol, err := s.repo.GetVolumeByID(ctx, volumeID)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}

	backupFile := filepath.Join(destDir, fmt.Sprintf("%s-%d.tar.gz", vol.Name, time.Now().Unix()))
	out, err := os.Create(backupFile)
	if err != nil {
		return "", err
	}
	defer out.Close()

	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	err = filepath.Walk(vol.HostPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(vol.HostPath, path)
		if relPath == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		header.Name = relPath
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed creating volume tar backup: %w", err)
	}

	return backupFile, nil
}

// RestoreVolume unpacks a .tar.gz snapshot into the volume directory.
func (s *Service) RestoreVolume(ctx context.Context, volumeID, backupFilePath string) error {
	vol, err := s.repo.GetVolumeByID(ctx, volumeID)
	if err != nil {
		return err
	}

	f, err := os.Open(backupFilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(vol.HostPath, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0755)
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				return err
			}
			_, _ = io.Copy(outFile, tr)
			outFile.Close()
		}
	}

	return nil
}

func (s *Service) ListVolumes(ctx context.Context, tenantID string) ([]*protocol.Volume, error) {
	return s.repo.ListVolumesByTenant(ctx, tenantID)
}

func (s *Service) GetVolume(ctx context.Context, id string) (*protocol.Volume, error) {
	return s.repo.GetVolumeByID(ctx, id)
}
