package dbng

import (
	"database/sql"
	"encoding/json"

	"code.cloudfoundry.org/lager"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/atc"
	"github.com/concourse/atc/db/lock"
	"github.com/lib/pq"
)

var EmptyParamsHash = mapHash(atc.Params{})

//go:generate counterfeiter . ResourceCacheFactory

type ResourceCacheFactory interface {
	FindOrCreateResourceCache(
		logger lager.Logger,
		resourceUser ResourceUser,
		resourceTypeName string,
		version atc.Version,
		source atc.Source,
		params atc.Params,
		resourceTypes atc.VersionedResourceTypes,
	) (*UsedResourceCache, error)

	CleanUsesForFinishedBuilds(lager.Logger) error
	CleanUsesForInactiveResourceTypes(lager.Logger) error
	CleanUsesForInactiveResources(lager.Logger) error
	CleanUsesForPausedPipelineResources(lager.Logger) error
	CleanUpInvalidCaches(lager.Logger) error
}

type resourceCacheFactory struct {
	conn        Conn
	lockFactory lock.LockFactory
}

func NewResourceCacheFactory(conn Conn, lockFactory lock.LockFactory) ResourceCacheFactory {
	return &resourceCacheFactory{
		conn:        conn,
		lockFactory: lockFactory,
	}
}

func (f *resourceCacheFactory) FindOrCreateResourceCache(
	logger lager.Logger,
	resourceUser ResourceUser,
	resourceTypeName string,
	version atc.Version,
	source atc.Source,
	params atc.Params,
	resourceTypes atc.VersionedResourceTypes,
) (*UsedResourceCache, error) {
	resourceConfig, err := constructResourceConfig(resourceTypeName, source, resourceTypes)
	if err != nil {
		return nil, err
	}

	resourceCache := ResourceCache{
		ResourceConfig: resourceConfig,
		Version:        version,
		Params:         params,
	}

	var usedResourceCache *UsedResourceCache

	err = safeFindOrCreate(f.conn, func(tx Tx) error {
		var err error
		usedResourceCache, err = resourceUser.UseResourceCache(logger, tx, f.lockFactory, resourceCache)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return usedResourceCache, nil
}

func (f *resourceCacheFactory) CleanUsesForFinishedBuilds(logger lager.Logger) error {
	latestImageResourceBuildByJobQ, _, err := sq.
		Select("MAX(b.id) AS max_build_id").
		From("image_resource_versions irv").
		Join("builds b ON b.id = irv.build_id").
		Where(sq.NotEq{"b.job_id": nil}).
		GroupBy("b.job_id").ToSql()
	if err != nil {
		return err
	}

	oneOffImagesUsedWithin24Hours, _, err := sq.
		Select("b.id").
		From("image_resource_versions irv").
		Join("builds b ON b.id = irv.build_id").
		Where(sq.Eq{"b.job_id": nil}).
		Where(sq.Expr("(now() - b.end_time) < '24 HOURS'::INTERVAL")).
		ToSql()
	if err != nil {
		return err
	}

	imageResourceCacheIds, imageResourceCacheArgs, err := sq.
		Select("rc.id").
		From("image_resource_versions irv").
		Join("resource_caches rc ON rc.version = irv.version").
		Join("resource_cache_uses rcu ON rcu.resource_cache_id = rc.id").
		Where(sq.Expr("irv.build_id = rcu.build_id")).
		Where(sq.Eq{"rc.params_hash": EmptyParamsHash}).
		Where(sq.Or{
			sq.Expr("irv.build_id IN (" + latestImageResourceBuildByJobQ + ")"),
			sq.Expr("irv.build_id IN (" + oneOffImagesUsedWithin24Hours + ")"),
		}).
		ToSql()
	if err != nil {
		return err
	}

	return f.logAndDeleteUses(
		logger,
		psql.Delete("resource_cache_uses rcu USING builds b").
			Where(sq.And{
				sq.Expr("rcu.build_id = b.id"),
				sq.Expr("b.interceptible = false"),
			}).
			Where("rcu.resource_cache_id NOT IN ("+imageResourceCacheIds+")", imageResourceCacheArgs...),
	)
}

func (f *resourceCacheFactory) CleanUsesForInactiveResourceTypes(logger lager.Logger) error {
	return f.logAndDeleteUses(
		logger,
		psql.Delete("resource_cache_uses rcu USING resource_types t").
			Where(sq.And{
				sq.Expr("rcu.resource_type_id = t.id"),
				sq.Eq{
					"t.active": false,
				},
			}),
	)
}

func (f *resourceCacheFactory) CleanUsesForInactiveResources(logger lager.Logger) error {
	return f.logAndDeleteUses(
		logger,
		psql.Delete("resource_cache_uses rcu USING resources r").
			Where(sq.And{
				sq.Expr("rcu.resource_id = r.id"),
				sq.Eq{
					"r.active": false,
				},
			}),
	)
}

func (f *resourceCacheFactory) CleanUpInvalidCaches(logger lager.Logger) error {
	stillInUseCacheIds, _, err := sq.
		Select("rc.id").
		Distinct().
		From("resource_caches rc").
		Join("resource_cache_uses rcu ON rc.id = rcu.resource_cache_id").
		ToSql()
	if err != nil {
		return err
	}

	cacheIdsForVolumes, cacheIdsForVolumesArgs, err := sq.
		Select("wrc.resource_cache_id").
		Distinct().
		From("volumes v").
		LeftJoin("worker_resource_caches wrc ON v.worker_resource_cache_id = wrc.id").
		Where(sq.NotEq{"v.worker_resource_cache_id": nil}).
		Where(sq.NotEq{"v.state": string(VolumeStateCreated)}).
		Where(sq.NotEq{"v.state": string(VolumeStateDestroying)}).
		ToSql()
	if err != nil {
		return err
	}

	nextBuildInputsCacheIds, _, err := sq.
		Select("rc.id").
		Distinct().
		From("next_build_inputs nbi").
		Join("versioned_resources vr ON vr.id = nbi.version_id").
		Join("resources r ON r.id = vr.resource_id").
		Join("resource_caches rc ON rc.version = vr.version").
		Join("resource_configs rf ON rc.resource_config_id = rf.id").
		Join("jobs j ON nbi.job_id = j.id").
		Join("pipelines p ON j.pipeline_id = p.id").
		Where(sq.Expr("r.source_hash = rf.source_hash")).
		Where(sq.Expr("p.paused = false")).
		ToSql()
	if err != nil {
		return err
	}

	delete := sq.Delete("resource_caches").
		Where("id NOT IN ("+nextBuildInputsCacheIds+")").
		Where("id NOT IN ("+stillInUseCacheIds+")").
		Where("id NOT IN ("+cacheIdsForVolumes+")", cacheIdsForVolumesArgs...).
		Suffix("RETURNING id, resource_config_id, version").
		PlaceholderFormat(sq.Dollar)

	rows, err := sq.QueryWith(f.conn, delete)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code.Name() == "foreign_key_violation" {
			// this can happen if a use or resource cache is created referencing the
			// config; as the subqueries above are not atomic
			return nil
		}

		return err
	}

	defer rows.Close()

	for rows.Next() {
		var id, resourceConfigID int
		var versionPayload []byte
		err := rows.Scan(&id, &resourceConfigID, &versionPayload)
		if err != nil {
			logger.Error("failed-to-scan-deleted-row", err)
			return err
		}

		var version atc.Version
		err = json.Unmarshal(versionPayload, &version)
		if err != nil {
			logger.Error("failed-to-unmarshal-version", err)
			return err
		}

		logger.Debug("deleted-resource-cache", lager.Data{
			"id":                 id,
			"resource-config-id": resourceConfigID,
			"version":            version,
		})
	}

	return nil
}

func (f *resourceCacheFactory) CleanUsesForPausedPipelineResources(logger lager.Logger) error {
	pausedPipelineIds, _, err := sq.
		Select("id").
		Distinct().
		From("pipelines").
		Where(sq.Expr("paused = false")).
		ToSql()
	if err != nil {
		return err
	}

	return f.logAndDeleteUses(
		logger,
		psql.Delete("resource_cache_uses rcu USING resources r").
			Where(sq.And{
				sq.Expr("r.pipeline_id NOT IN (" + pausedPipelineIds + ")"),
				sq.Expr("rcu.resource_id = r.id"),
			}),
	)
}

func (f *resourceCacheFactory) logAndDeleteUses(logger lager.Logger, delete sq.DeleteBuilder) error {
	delete = delete.Suffix("RETURNING resource_cache_id, build_id, resource_id, resource_type_id")

	rows, err := sq.QueryWith(f.conn, delete)
	if err != nil {
		return err
	}

	defer rows.Close()

	for rows.Next() {
		var resourceCacheID int
		var buildID, resourceID, resourceTypeID sql.NullInt64
		err := rows.Scan(&resourceCacheID, &buildID, &resourceID, &resourceTypeID)
		if err != nil {
			logger.Error("failed-to-scan-deleted-row", err)
			return err
		}

		data := lager.Data{
			"resource-cache-id": resourceCacheID,
		}

		if buildID.Valid {
			data["build-id"] = buildID.Int64
		}

		if resourceID.Valid {
			data["resource-id"] = resourceID.Int64
		}

		if resourceTypeID.Valid {
			data["resource-type-id"] = resourceTypeID.Int64
		}

		logger.Debug("deleted-resource-cache-use", data)
	}

	return nil
}
