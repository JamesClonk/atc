package worker

import (
	"io"

	"github.com/concourse/atc/dbng"
	"github.com/concourse/baggageclaim"
)

//go:generate counterfeiter . Volume

type Volume interface {
	Handle() string
	Path() string

	SetProperty(key string, value string) error
	Properties() (baggageclaim.VolumeProperties, error)

	SetPrivileged(bool) error

	StreamIn(path string, tarStream io.Reader) error
	StreamOut(path string) (io.ReadCloser, error)

	COWStrategy() baggageclaim.COWStrategy

	IsInitialized() (bool, error)
	Initialize() error

	CreateChildForContainer(dbng.CreatingContainer, string) (dbng.CreatingVolume, error)

	Destroy() error
}

type VolumeMount struct {
	Volume    Volume
	MountPath string
}

type volume struct {
	bcVolume baggageclaim.Volume
	dbVolume dbng.CreatedVolume
}

func NewVolume(
	bcVolume baggageclaim.Volume,
	dbVolume dbng.CreatedVolume,
) Volume {
	return &volume{
		bcVolume: bcVolume,
		dbVolume: dbVolume,
	}
}

func (v *volume) Handle() string { return v.bcVolume.Handle() }

func (v *volume) Path() string { return v.bcVolume.Path() }

func (v *volume) SetProperty(key string, value string) error {
	return v.bcVolume.SetProperty(key, value)
}

func (v *volume) SetPrivileged(privileged bool) error {
	return v.bcVolume.SetPrivileged(privileged)
}

func (v *volume) StreamIn(path string, tarStream io.Reader) error {
	return v.bcVolume.StreamIn(path, tarStream)
}

func (v *volume) StreamOut(path string) (io.ReadCloser, error) {
	return v.bcVolume.StreamOut(path)
}

func (v *volume) Properties() (baggageclaim.VolumeProperties, error) {
	return v.bcVolume.Properties()
}

func (v *volume) Destroy() error {
	return v.bcVolume.Destroy()
}

func (v *volume) COWStrategy() baggageclaim.COWStrategy {
	return baggageclaim.COWStrategy{
		Parent: v.bcVolume,
	}
}

func (v *volume) IsInitialized() (bool, error) {
	return v.dbVolume.IsInitialized()
}

func (v *volume) Initialize() error {
	return v.dbVolume.Initialize()
}

func (v *volume) CreateChildForContainer(creatingContainer dbng.CreatingContainer, mountPath string) (dbng.CreatingVolume, error) {
	return v.dbVolume.CreateChildForContainer(creatingContainer, mountPath)
}
