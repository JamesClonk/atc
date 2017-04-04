package image

import (
	"io"
	"net/url"
	"path"

	"code.cloudfoundry.org/lager"
	"github.com/concourse/atc"
	"github.com/concourse/atc/dbng"
	"github.com/concourse/atc/worker"
	"github.com/concourse/baggageclaim"
)

const RawRootFSScheme = "raw"

type imageProvidedByPreviousStepOnSameWorker struct {
	artifactVolume worker.Volume
	imageSpec      worker.ImageSpec
	teamID         int
	volumeClient   worker.VolumeClient
}

func (i *imageProvidedByPreviousStepOnSameWorker) FetchForContainer(
	logger lager.Logger,
	container dbng.CreatingContainer,
) (worker.FetchedImage, error) {
	imageVolume, err := i.volumeClient.FindOrCreateCOWVolumeForContainer(
		logger,
		worker.VolumeSpec{
			Strategy:   i.artifactVolume.COWStrategy(),
			Privileged: i.imageSpec.Privileged,
		},
		container,
		i.artifactVolume,
		i.teamID,
		"/",
	)
	if err != nil {
		logger.Error("failed-to-create-image-artifact-cow-volume", err)
		return worker.FetchedImage{}, err
	}

	imageMetadataReader, err := i.imageSpec.ImageArtifactSource.StreamFile(ImageMetadataFile)
	if err != nil {
		logger.Error("failed-to-stream-metadata-file", err)
		return worker.FetchedImage{}, err
	}

	metadata, err := loadMetadata(imageMetadataReader)
	if err != nil {
		return worker.FetchedImage{}, err
	}

	imageURL := url.URL{
		Scheme: RawRootFSScheme,
		Path:   path.Join(imageVolume.Path(), "rootfs"),
	}

	return worker.FetchedImage{
		Metadata: metadata,
		URL:      imageURL.String(),
	}, nil
}

type imageProvidedByPreviousStepOnDifferentWorker struct {
	imageSpec    worker.ImageSpec
	teamID       int
	volumeClient worker.VolumeClient
}

func (i *imageProvidedByPreviousStepOnDifferentWorker) FetchForContainer(
	logger lager.Logger,
	container dbng.CreatingContainer,
) (worker.FetchedImage, error) {
	imageVolume, err := i.volumeClient.FindOrCreateVolumeForContainer(
		logger,
		worker.VolumeSpec{
			Strategy:   baggageclaim.EmptyStrategy{},
			Privileged: i.imageSpec.Privileged,
		},
		container,
		i.teamID,
		"/",
	)
	if err != nil {
		logger.Error("failed-to-create-image-artifact-replicated-volume", err)
		return worker.FetchedImage{}, nil
	}

	dest := artifactDestination{
		destination: imageVolume,
	}

	err = i.imageSpec.ImageArtifactSource.StreamTo(&dest)
	if err != nil {
		logger.Error("failed-to-stream-image-artifact-source", err)
		return worker.FetchedImage{}, nil
	}

	imageMetadataReader, err := i.imageSpec.ImageArtifactSource.StreamFile(ImageMetadataFile)
	if err != nil {
		logger.Error("failed-to-stream-metadata-file", err)
		return worker.FetchedImage{}, err
	}

	metadata, err := loadMetadata(imageMetadataReader)
	if err != nil {
		return worker.FetchedImage{}, err
	}

	imageURL := url.URL{
		Scheme: RawRootFSScheme,
		Path:   path.Join(imageVolume.Path(), "rootfs"),
	}

	return worker.FetchedImage{
		Metadata: metadata,
		URL:      imageURL.String(),
	}, nil
}

type imageFromResource struct {
	imageParentVolume   worker.Volume
	version             atc.Version
	imageMetadataReader io.ReadCloser
	imageSpec           worker.ImageSpec
	teamID              int
	volumeClient        worker.VolumeClient
}

func (i *imageFromResource) FetchForContainer(
	logger lager.Logger,
	container dbng.CreatingContainer,
) (worker.FetchedImage, error) {
	imageVolume, err := i.volumeClient.FindOrCreateCOWVolumeForContainer(
		logger.Session("create-cow-volume"),
		worker.VolumeSpec{
			Strategy:   i.imageParentVolume.COWStrategy(),
			Privileged: i.imageSpec.Privileged,
		},
		container,
		i.imageParentVolume,
		i.teamID,
		"/",
	)
	if err != nil {
		logger.Error("failed-to-create-image-resource-volume", err)
		return worker.FetchedImage{}, err
	}

	metadata, err := loadMetadata(i.imageMetadataReader)
	if err != nil {
		return worker.FetchedImage{}, err
	}

	imageURL := url.URL{
		Scheme: RawRootFSScheme,
		Path:   path.Join(imageVolume.Path(), "rootfs"),
	}

	return worker.FetchedImage{
		Metadata: metadata,
		Version:  i.version,
		URL:      imageURL.String(),
	}, nil
}

type imageFromBaseResourceType struct {
	worker           worker.Worker
	resourceTypeName string
	teamID           int
	volumeClient     worker.VolumeClient
}

func (i *imageFromBaseResourceType) FetchForContainer(
	logger lager.Logger,
	container dbng.CreatingContainer,
) (worker.FetchedImage, error) {
	for _, t := range i.worker.ResourceTypes() {
		if t.Type == i.resourceTypeName {
			importVolume, err := i.volumeClient.FindOrCreateVolumeForBaseResourceType(
				logger,
				worker.VolumeSpec{
					Strategy:   baggageclaim.ImportStrategy{Path: t.Image},
					Privileged: true,
				},
				i.teamID,
				i.resourceTypeName,
			)
			if err != nil {
				return worker.FetchedImage{}, err
			}

			cowVolume, err := i.volumeClient.FindOrCreateCOWVolumeForContainer(
				logger,
				worker.VolumeSpec{
					Strategy:   importVolume.COWStrategy(),
					Privileged: true,
				},
				container,
				importVolume,
				i.teamID,
				"/",
			)
			if err != nil {
				return worker.FetchedImage{}, err
			}

			rootFSURL := url.URL{
				Scheme: RawRootFSScheme,
				Path:   cowVolume.Path(),
			}

			return worker.FetchedImage{
				Metadata: worker.ImageMetadata{},
				Version:  atc.Version{i.resourceTypeName: t.Version},
				URL:      rootFSURL.String(),
			}, nil
		}
	}

	return worker.FetchedImage{}, ErrUnsupportedResourceType
}

type imageInTask struct {
	url string
}

func (i *imageInTask) FetchForContainer(
	logger lager.Logger,
	container dbng.CreatingContainer,
) (worker.FetchedImage, error) {
	return worker.FetchedImage{
		URL: i.url,
	}, nil
}

type artifactDestination struct {
	destination worker.Volume
}

func (wad *artifactDestination) StreamIn(path string, tarStream io.Reader) error {
	return wad.destination.StreamIn(path, tarStream)
}
