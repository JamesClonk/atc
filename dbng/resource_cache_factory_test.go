package dbng_test

import (
	"crypto/sha256"
	"database/sql"
	"fmt"

	"code.cloudfoundry.org/lager/lagertest"

	"github.com/concourse/atc"
	"github.com/concourse/atc/dbng"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceCacheFactory", func() {
	var (
		usedBaseResourceType      *dbng.UsedBaseResourceType
		usedImageBaseResourceType *dbng.UsedBaseResourceType

		resourceType1                  atc.VersionedResourceType
		resourceType2                  atc.VersionedResourceType
		resourceType3                  atc.VersionedResourceType
		resourceTypeUsingBogusBaseType atc.VersionedResourceType
		resourceTypeOverridingBaseType atc.VersionedResourceType

		logger *lagertest.TestLogger
	)

	BeforeEach(func() {
		setupTx, err := dbConn.Begin()
		Expect(err).ToNot(HaveOccurred())

		baseResourceType := dbng.BaseResourceType{
			Name: "some-base-type",
		}

		usedBaseResourceType, err = baseResourceType.FindOrCreate(setupTx)
		Expect(err).NotTo(HaveOccurred())

		imageBaseResourceType := dbng.BaseResourceType{
			Name: "some-image-type",
		}

		usedImageBaseResourceType, err = imageBaseResourceType.FindOrCreate(setupTx)
		Expect(err).NotTo(HaveOccurred())

		Expect(setupTx.Commit()).To(Succeed())

		resourceType1 = atc.VersionedResourceType{
			ResourceType: atc.ResourceType{
				Name: "some-type",
				Type: "some-type-type",
				Source: atc.Source{
					"some-type": "source",
				},
			},
			Version: atc.Version{"some-type": "version"},
		}

		resourceType2 = atc.VersionedResourceType{
			ResourceType: atc.ResourceType{
				Name: "some-type-type",
				Type: "some-base-type",
				Source: atc.Source{
					"some-type-type": "source",
				},
			},
			Version: atc.Version{"some-type-type": "version"},
		}

		resourceType3 = atc.VersionedResourceType{
			ResourceType: atc.ResourceType{
				Name: "some-unused-type",
				Type: "some-base-type",
				Source: atc.Source{
					"some-unused-type": "source",
				},
			},
			Version: atc.Version{"some-unused-type": "version"},
		}

		resourceTypeUsingBogusBaseType = atc.VersionedResourceType{
			ResourceType: atc.ResourceType{
				Name: "some-type-using-bogus-base-type",
				Type: "some-bogus-base-type",
				Source: atc.Source{
					"some-type-using-bogus-base-type": "source",
				},
			},
			Version: atc.Version{"some-type-using-bogus-base-type": "version"},
		}

		resourceTypeOverridingBaseType = atc.VersionedResourceType{
			ResourceType: atc.ResourceType{
				Name: "some-image-type",
				Type: "some-image-type",
				Source: atc.Source{
					"some-image-type": "source",
				},
			},
			Version: atc.Version{"some-image-type": "version"},
		}

		pipelineWithTypes, _, err := defaultTeam.SavePipeline(
			"pipeline-with-types",
			atc.Config{
				ResourceTypes: atc.ResourceTypes{
					resourceType1.ResourceType,
					resourceType2.ResourceType,
					resourceType3.ResourceType,
					resourceTypeUsingBogusBaseType.ResourceType,
					resourceTypeOverridingBaseType.ResourceType,
				},
			},
			dbng.ConfigVersion(0),
			dbng.PipelineUnpaused,
		)
		Expect(err).ToNot(HaveOccurred())

		for _, rt := range []atc.VersionedResourceType{
			resourceType1,
			resourceType2,
			resourceType3,
			resourceTypeUsingBogusBaseType,
			resourceTypeOverridingBaseType,
		} {
			dbType, found, err := pipelineWithTypes.ResourceType("some-type")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			err = dbType.SaveVersion(rt.Version)
			Expect(err).NotTo(HaveOccurred())
		}

		logger = lagertest.NewTestLogger("test")
	})

	AfterEach(func() {
		err := dbConn.Close()
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("FindOrCreateResourceCache", func() {
		It("creates resource cache in database", func() {
			usedResourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				logger,
				dbng.ForBuild(defaultBuild.ID()),
				"some-type",
				atc.Version{"some": "version"},
				atc.Source{
					"some": "source",
				},
				atc.Params{"some": "params"},
				atc.VersionedResourceTypes{
					resourceType1,
					resourceType2,
					resourceType3,
				},
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(usedResourceCache.Version).To(Equal(atc.Version{"some": "version"}))

			rows, err := psql.Select("a.version, a.params_hash, o.source_hash, b.name").
				From("resource_caches a").
				LeftJoin("resource_configs o ON a.resource_config_id = o.id").
				LeftJoin("base_resource_types b ON o.base_resource_type_id = b.id").
				RunWith(dbConn).
				Query()
			Expect(err).NotTo(HaveOccurred())
			resourceCaches := []resourceCache{}
			for rows.Next() {
				var version string
				var paramsHash string
				var sourceHash sql.NullString
				var baseResourceTypeName sql.NullString

				err := rows.Scan(&version, &paramsHash, &sourceHash, &baseResourceTypeName)
				Expect(err).NotTo(HaveOccurred())

				var sourceHashString string
				if sourceHash.Valid {
					sourceHashString = sourceHash.String
				}

				var baseResourceTypeNameString string
				if baseResourceTypeName.Valid {
					baseResourceTypeNameString = baseResourceTypeName.String
				}

				resourceCaches = append(resourceCaches, resourceCache{
					Version:          version,
					ParamsHash:       paramsHash,
					SourceHash:       sourceHashString,
					BaseResourceName: baseResourceTypeNameString,
				})
			}

			var toHash = func(s string) string {
				return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))
			}

			Expect(resourceCaches).To(ConsistOf(
				resourceCache{
					Version:          `{"some-type-type":"version"}`,
					ParamsHash:       toHash(`{}`),
					BaseResourceName: "some-base-type",
					SourceHash:       toHash(`{"some-type-type":"source"}`),
				},
				resourceCache{
					Version:    `{"some-type":"version"}`,
					ParamsHash: toHash(`{}`),
					SourceHash: toHash(`{"some-type":"source"}`),
				},
				resourceCache{
					Version:    `{"some":"version"}`,
					ParamsHash: toHash(`{"some":"params"}`),
					SourceHash: toHash(`{"some":"source"}`),
				},
			))
		})

		It("returns an error if base resource type does not exist", func() {
			_, err := resourceCacheFactory.FindOrCreateResourceCache(
				logger,
				dbng.ForBuild(defaultBuild.ID()),
				"some-type-using-bogus-base-type",
				atc.Version{"some": "version"},
				atc.Source{
					"some": "source",
				},
				atc.Params{"some": "params"},
				atc.VersionedResourceTypes{
					resourceType1,
					resourceTypeUsingBogusBaseType,
				},
			)
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(dbng.ErrBaseResourceTypeNotFound))
		})

		It("allows a base resource type to be overridden using itself", func() {
			usedResourceCache, err := resourceCacheFactory.FindOrCreateResourceCache(
				logger,
				dbng.ForBuild(defaultBuild.ID()),
				"some-image-type",
				atc.Version{"some": "version"},
				atc.Source{
					"some": "source",
				},
				atc.Params{"some": "params"},
				atc.VersionedResourceTypes{
					resourceTypeOverridingBaseType,
				},
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(usedResourceCache.Version).To(Equal(atc.Version{"some": "version"}))
			Expect(usedResourceCache.ResourceConfig.CreatedByResourceCache.Version).To(Equal(atc.Version{"some-image-type": "version"}))
			Expect(usedResourceCache.ResourceConfig.CreatedByResourceCache.ResourceConfig.CreatedByBaseResourceType.ID).To(Equal(usedImageBaseResourceType.ID))
		})
	})

	Describe("CleanUpInvalidCaches", func() {
		countResourceCaches := func() int {
			var result int
			err = psql.Select("count(*)").
				From("resource_caches").
				RunWith(dbConn).
				QueryRow().
				Scan(&result)
			Expect(err).NotTo(HaveOccurred())

			return result
		}

		Context("when there is resource cache", func() {
			var usedResourceCache *dbng.UsedResourceCache
			var build dbng.Build

			BeforeEach(func() {
				build, err = defaultTeam.CreateOneOffBuild()
				Expect(err).ToNot(HaveOccurred())

				setupTx, err := dbConn.Begin()
				Expect(err).ToNot(HaveOccurred())
				defer setupTx.Rollback()

				resourceCache := dbng.ResourceCache{
					ResourceConfig: dbng.ResourceConfig{
						CreatedByBaseResourceType: &dbng.BaseResourceType{
							Name: "some-base-resource-type",
						},
					},
				}
				usedResourceCache, err = dbng.ForBuild(build.ID()).UseResourceCache(logger, setupTx, lockFactory, resourceCache)
				Expect(err).NotTo(HaveOccurred())
				Expect(setupTx.Commit()).To(Succeed())
			})

			Context("when resource cache is not used any more", func() {
				BeforeEach(func() {
					_, err := build.Delete()
					Expect(err).NotTo(HaveOccurred())
				})

				It("deletes the resource cache", func() {
					err := resourceCacheFactory.CleanUpInvalidCaches()
					Expect(err).NotTo(HaveOccurred())
					Expect(countResourceCaches()).To(BeZero())
				})

				Context("when there is a volume for resource cache in creating state", func() {
					BeforeEach(func() {
						_, err := volumeFactory.CreateResourceCacheVolume(defaultWorker, usedResourceCache)
						Expect(err).NotTo(HaveOccurred())
					})

					It("does not delete the cache", func() {
						err := resourceCacheFactory.CleanUpInvalidCaches()
						Expect(err).NotTo(HaveOccurred())
						Expect(countResourceCaches()).To(Equal(1))
					})
				})

				Context("when there is a volume for resource cache in created state", func() {
					BeforeEach(func() {
						creatingVolume, err := volumeFactory.CreateResourceCacheVolume(defaultWorker, usedResourceCache)
						Expect(err).NotTo(HaveOccurred())
						_, err = creatingVolume.Created()
						Expect(err).NotTo(HaveOccurred())
					})

					It("deletes the cache", func() {
						err := resourceCacheFactory.CleanUpInvalidCaches()
						Expect(err).NotTo(HaveOccurred())
						Expect(countResourceCaches()).To(Equal(0))
					})
				})

				Context("when there is a volume for resource cache in destroying state", func() {
					BeforeEach(func() {
						creatingVolume, err := volumeFactory.CreateResourceCacheVolume(defaultWorker, usedResourceCache)
						Expect(err).NotTo(HaveOccurred())
						createdVolume, err := creatingVolume.Created()
						Expect(err).NotTo(HaveOccurred())
						_, err = createdVolume.Destroying()
						Expect(err).NotTo(HaveOccurred())
					})

					It("deletes the cache", func() {
						err := resourceCacheFactory.CleanUpInvalidCaches()
						Expect(err).NotTo(HaveOccurred())
						Expect(countResourceCaches()).To(Equal(0))
					})
				})
			})
		})
	})
})

type resourceCache struct {
	Version          string
	ParamsHash       string
	SourceHash       string
	BaseResourceName string
}
