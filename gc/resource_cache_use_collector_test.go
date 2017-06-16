package gc_test

import (
	"code.cloudfoundry.org/lager/lagertest"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/atc"
	"github.com/concourse/atc/db"
	"github.com/concourse/atc/gc"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("ResourceCacheUseCollector", func() {
	var collector gc.Collector
	var buildCollector gc.Collector

	BeforeEach(func() {
		logger := lagertest.NewTestLogger("resource-cache-use-collector")
		collector = gc.NewResourceCacheUseCollector(logger, resourceCacheFactory)
		buildCollector = gc.NewBuildCollector(logger, buildFactory)
	})

	Describe("Run", func() {
		Describe("cache uses", func() {
			var (
				pipelineWithTypes     db.Pipeline
				versionedResourceType atc.VersionedResourceType
				dbResourceType        db.ResourceType
			)

			countResourceCacheUses := func() int {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer tx.Rollback()

				var result int
				err = psql.Select("count(*)").
					From("resource_cache_uses").
					RunWith(tx).
					QueryRow().
					Scan(&result)
				Expect(err).NotTo(HaveOccurred())

				return result
			}

			BeforeEach(func() {
				versionedResourceType = atc.VersionedResourceType{
					ResourceType: atc.ResourceType{
						Name: "some-type",
						Type: "some-base-type",
						Source: atc.Source{
							"some-type": "source",
						},
					},
					Version: atc.Version{"some-type": "version"},
				}

				var created bool
				var err error
				pipelineWithTypes, created, err = defaultTeam.SavePipeline(
					"pipeline-with-types",
					atc.Config{
						ResourceTypes: atc.ResourceTypes{versionedResourceType.ResourceType},
					},
					0,
					db.PipelineNoChange,
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(created).To(BeTrue())

				var found bool
				dbResourceType, found, err = pipelineWithTypes.ResourceType("some-type")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(dbResourceType.SaveVersion(versionedResourceType.Version)).To(Succeed())
				Expect(dbResourceType.Reload()).To(BeTrue())
			})

			Describe("for one-off builds", func() {
				BeforeEach(func() {
					_, err = resourceCacheFactory.FindOrCreateResourceCache(
						logger,
						db.ForBuild(defaultBuild.ID()),
						"some-type",
						atc.Version{"some": "version"},
						atc.Source{
							"some": "source",
						},
						atc.Params{"some": "params"},
						atc.VersionedResourceTypes{
							versionedResourceType,
						},
					)
					Expect(err).NotTo(HaveOccurred())
				})

				Context("before the build has completed", func() {
					It("does not clean up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).NotTo(BeZero())
					})
				})

				Context("once the build has completed successfully", func() {
					It("cleans up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						Expect(defaultBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
						Expect(buildCollector.Run()).To(Succeed())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).To(BeZero())
					})
				})

				Context("once the build has been aborted", func() {
					It("cleans up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						Expect(defaultBuild.Finish(db.BuildStatusAborted)).To(Succeed())
						Expect(buildCollector.Run()).To(Succeed())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).To(BeZero())
					})
				})

				Context("once the build has failed", func() {
					Context("when the build is a one-off", func() {
						It("cleans up the uses", func() {
							Expect(countResourceCacheUses()).NotTo(BeZero())
							Expect(defaultBuild.Finish(db.BuildStatusFailed)).To(Succeed())
							Expect(buildCollector.Run()).To(Succeed())
							Expect(collector.Run()).To(Succeed())
							Expect(countResourceCacheUses()).To(BeZero())
						})
					})

					Context("when the build is for a job", func() {
						var jobId int

						BeforeEach(func() {
							tx, err := dbConn.Begin()
							Expect(err).NotTo(HaveOccurred())
							defer tx.Rollback()
							err = psql.Insert("jobs").
								Columns("name", "pipeline_id", "config").
								Values("lousy-job", pipelineWithTypes.ID(), `{"some":"config"}`).
								Suffix("RETURNING id").
								RunWith(tx).QueryRow().Scan(&jobId)
							Expect(err).NotTo(HaveOccurred())
							Expect(tx.Commit()).To(Succeed())
						})

						JustBeforeEach(func() {
							tx, err := dbConn.Begin()
							Expect(err).NotTo(HaveOccurred())
							defer tx.Rollback()
							_, err = psql.Update("builds").
								SetMap(map[string]interface{}{
									"status":    "failed",
									"end_time":  sq.Expr("NOW()"),
									"completed": true,
									"job_id":    jobId,
								}).
								RunWith(tx).Exec()
							Expect(err).NotTo(HaveOccurred())
							Expect(tx.Commit()).To(Succeed())
						})

						Context("when it is the latest failed build", func() {
							It("preserves the uses", func() {
								Expect(countResourceCacheUses()).NotTo(BeZero())
								Expect(defaultBuild.Finish(db.BuildStatusFailed)).To(Succeed())
								Expect(buildCollector.Run()).To(Succeed())
								Expect(collector.Run()).To(Succeed())
								Expect(countResourceCacheUses()).NotTo(BeZero())
							})
						})

						Context("when a later build of the same job has failed also", func() {
							BeforeEach(func() {
								_, err = defaultTeam.CreateOneOffBuild()
								Expect(err).NotTo(HaveOccurred())
							})

							It("cleans up the uses", func() {
								Expect(countResourceCacheUses()).NotTo(BeZero())
								Expect(buildCollector.Run()).To(Succeed())
								Expect(collector.Run()).To(Succeed())
								Expect(countResourceCacheUses()).To(BeZero())
							})
						})
					})
				})
			})

			Describe("for resource types", func() {
				setActiveResourceType := func(active bool) {
					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					defer tx.Rollback()

					var id int
					err = psql.Update("resource_types").
						Set("active", active).
						Where(sq.Eq{"id": dbResourceType.ID()}).
						Suffix("RETURNING id").
						RunWith(tx).
						QueryRow().Scan(&id)
					Expect(err).NotTo(HaveOccurred())

					err = tx.Commit()
					Expect(err).NotTo(HaveOccurred())
				}

				BeforeEach(func() {
					_, err = resourceCacheFactory.FindOrCreateResourceCache(
						logger,
						db.ForResourceType(dbResourceType.ID()),
						"some-type",
						atc.Version{"some-type": "version"},
						atc.Source{
							"cache": "source",
						},
						atc.Params{"some": "params"},
						atc.VersionedResourceTypes{
							versionedResourceType,
						},
					)
					Expect(err).NotTo(HaveOccurred())
					setActiveResourceType(true)
				})

				Context("while the resource type is still active", func() {
					It("does not clean up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).NotTo(BeZero())
					})
				})

				Context("once the resource type is made inactive", func() {
					It("cleans up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						setActiveResourceType(false)
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).To(BeZero())
					})
				})
			})

			Describe("for resources", func() {
				setActiveResource := func(resource db.Resource, active bool) {
					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					defer tx.Rollback()

					var id int
					err = psql.Update("resources").
						Set("active", active).
						Where(sq.Eq{"id": resource.ID()}).
						Suffix("RETURNING id").
						RunWith(tx).
						QueryRow().Scan(&id)
					Expect(err).NotTo(HaveOccurred())

					err = tx.Commit()
					Expect(err).NotTo(HaveOccurred())
				}

				BeforeEach(func() {
					_, err = resourceCacheFactory.FindOrCreateResourceCache(
						logger,
						db.ForResource(usedResource.ID()),
						"some-type",
						atc.Version{"some-type": "version"},
						atc.Source{
							"cache": "source",
						},
						atc.Params{"some": "params"},
						atc.VersionedResourceTypes{
							versionedResourceType,
						},
					)
					Expect(err).NotTo(HaveOccurred())
					setActiveResource(usedResource, true)
				})

				Context("while the resource is still active", func() {
					It("does not clean up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).NotTo(BeZero())
					})
				})

				Context("once the resource is made inactive", func() {
					It("cleans up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						setActiveResource(usedResource, false)
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).To(BeZero())
					})
				})

				Context("when the associated pipeline is paused", func() {
					It("cleans up the uses", func() {
						Expect(countResourceCacheUses()).NotTo(BeZero())
						err := defaultPipeline.Pause()
						Expect(err).NotTo(HaveOccurred())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).To(BeZero())
					})
				})
				Context("when a portion of pipelines referencing config are paused", func() {
					BeforeEach(func() {
						anotherPipeline, created, err := defaultTeam.SavePipeline(
							"another-pipeline",
							atc.Config{
								Resources: []atc.ResourceConfig{
									{
										Name:   "another-resource",
										Type:   usedResource.Type(),
										Source: usedResource.RawSource(),
									},
								},
							},
							0,
							db.PipelineUnpaused,
						)

						Expect(err).ToNot(HaveOccurred())
						Expect(created).To(BeTrue())

						anotherResource, found, err := anotherPipeline.Resource("another-resource")
						Expect(err).ToNot(HaveOccurred())
						Expect(found).To(BeTrue())

						_, err = resourceCacheFactory.FindOrCreateResourceCache(
							logger,
							db.ForResource(anotherResource.ID()),
							"some-type",
							atc.Version{"some-type": "version"},
							atc.Source{
								"cache": "source",
							},
							atc.Params{"some": "params"},
							atc.VersionedResourceTypes{versionedResourceType},
						)
						Expect(err).NotTo(HaveOccurred())
						setActiveResource(anotherResource, true)
					})

					It("does not clean up the uses for unpaused pipeline resources", func() {
						Expect(countResourceCacheUses()).To(Equal(4))
						err := defaultPipeline.Pause()
						Expect(err).NotTo(HaveOccurred())
						Expect(collector.Run()).To(Succeed())
						Expect(countResourceCacheUses()).To(Equal(2))
					})
				})
			})
		})
	})
})
