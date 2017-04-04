package resource_test

import (
	"errors"

	"code.cloudfoundry.org/lager"
	"code.cloudfoundry.org/lager/lagertest"
	"github.com/concourse/atc"
	"github.com/concourse/atc/dbng"
	"github.com/concourse/atc/dbng/dbngfakes"
	. "github.com/concourse/atc/resource"
	"github.com/concourse/atc/worker"
	"github.com/concourse/atc/worker/workerfakes"
	"github.com/concourse/baggageclaim"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceInstance", func() {
	var (
		logger                   lager.Logger
		resourceInstance         ResourceInstance
		fakeWorkerClient         *workerfakes.FakeClient
		fakeResourceCacheFactory *dbngfakes.FakeResourceCacheFactory
		disaster                 error
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")
		fakeWorkerClient = new(workerfakes.FakeClient)
		fakeResourceCacheFactory = new(dbngfakes.FakeResourceCacheFactory)
		disaster = errors.New("disaster")

		resourceInstance = NewResourceInstance(
			"some-resource-type",
			atc.Version{"some": "version"},
			atc.Source{"some": "source"},
			atc.Params{"some": "params"},
			dbng.ForBuild(42),
			atc.VersionedResourceTypes{},
			fakeResourceCacheFactory,
		)
	})

	Describe("FindInitializedOn", func() {
		var (
			foundVolume worker.Volume
			found       bool
			findErr     error
		)

		JustBeforeEach(func() {
			foundVolume, found, findErr = resourceInstance.FindInitializedOn(logger, fakeWorkerClient)
		})

		It("'find-or-create's the resource cache with the same user", func() {
			_, user, _, _, _, _, _ := fakeResourceCacheFactory.FindOrCreateResourceCacheArgsForCall(0)
			Expect(user).To(Equal(dbng.ForBuild(42)))
		})

		Context("when failing to find or create cache in database", func() {
			BeforeEach(func() {
				fakeResourceCacheFactory.FindOrCreateResourceCacheReturns(nil, disaster)
			})

			It("returns the error", func() {
				Expect(findErr).To(Equal(disaster))
			})
		})

		Context("when initialized volume for resource cache exists on worker", func() {
			var fakeVolume *workerfakes.FakeVolume

			BeforeEach(func() {
				fakeVolume = new(workerfakes.FakeVolume)
				fakeWorkerClient.FindInitializedVolumeForResourceCacheReturns(fakeVolume, true, nil)
			})

			It("returns found volume", func() {
				Expect(findErr).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(foundVolume).To(Equal(fakeVolume))
			})
		})

		Context("when initialized volume for resource cache does not exist on worker", func() {
			BeforeEach(func() {
				fakeWorkerClient.FindInitializedVolumeForResourceCacheReturns(nil, false, nil)
			})

			It("does not return any volume", func() {
				Expect(findErr).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(foundVolume).To(BeNil())
			})
		})

		Context("when worker errors in finding the cache", func() {
			BeforeEach(func() {
				fakeWorkerClient.FindInitializedVolumeForResourceCacheReturns(nil, false, disaster)
			})

			It("returns the error", func() {
				Expect(findErr).To(Equal(disaster))
				Expect(found).To(BeFalse())
				Expect(foundVolume).To(BeNil())
			})
		})
	})

	Context("CreateOn", func() {
		var createdVolume worker.Volume
		var createErr error

		JustBeforeEach(func() {
			createdVolume, createErr = resourceInstance.CreateOn(logger, fakeWorkerClient)
		})

		Context("when creating the volume succeeds", func() {
			var volume *workerfakes.FakeVolume

			BeforeEach(func() {
				volume = new(workerfakes.FakeVolume)
				fakeWorkerClient.CreateVolumeForResourceCacheReturns(volume, nil)
			})

			It("succeeds", func() {
				Expect(createErr).ToNot(HaveOccurred())
			})

			It("returns the volume", func() {
				Expect(createdVolume).To(Equal(volume))
			})

			It("created with the right strategy and privileges", func() {
				_, spec, _ := fakeWorkerClient.CreateVolumeForResourceCacheArgsForCall(0)
				Expect(spec).To(Equal(worker.VolumeSpec{
					Strategy:   baggageclaim.EmptyStrategy{},
					Privileged: true,
				}))
			})
		})

		Context("when creating the volume fails", func() {
			BeforeEach(func() {
				fakeWorkerClient.CreateVolumeForResourceCacheReturns(nil, disaster)
			})

			It("returns the error", func() {
				Expect(createErr).To(Equal(disaster))
			})
		})
	})

	Context("ResourceCacheIdentifier", func() {
		It("returns a volume identifier corrsponding to the resource that the identifier is tracking", func() {
			expectedIdentifier := worker.ResourceCacheIdentifier{
				ResourceVersion: atc.Version{"some": "version"},
				ResourceHash:    `some-resource-type{"some":"source"}`,
			}

			Expect(resourceInstance.ResourceCacheIdentifier()).To(Equal(expectedIdentifier))
		})
	})
})

var _ = Describe("GenerateResourceHash", func() {
	It("returns a hash of the source and resource type", func() {
		Expect(GenerateResourceHash(atc.Source{"some": "source"}, "git")).To(Equal(`git{"some":"source"}`))
	})
})
