package dbng

import (
	"database/sql"
	"log"

	sq "github.com/Masterminds/squirrel"
)

//go:generate counterfeiter . ContainerFactory

type ContainerFactory interface {
	FindContainersForDeletion() ([]CreatingContainer, []CreatedContainer, []DestroyingContainer, error)
}

type containerFactory struct {
	conn Conn
}

func NewContainerFactory(conn Conn) ContainerFactory {
	return &containerFactory{
		conn: conn,
	}
}

func (factory *containerFactory) FindContainersForDeletion() ([]CreatingContainer, []CreatedContainer, []DestroyingContainer, error) {
	query, args, err := selectContainers("c").
		LeftJoin("builds b ON b.id = c.build_id").
		LeftJoin("volumes v ON v.worker_resource_cache_id = c.worker_resource_cache_id").
		LeftJoin("worker_resource_caches wrc ON wrc.id = c.worker_resource_cache_id").
		LeftJoin("(select resource_cache_id, count(*) cnt from resource_cache_uses GROUP BY resource_cache_id) rcu ON rcu.resource_cache_id = wrc.resource_cache_id").
		Where(sq.Or{
			sq.Expr("(c.build_id IS NOT NULL AND b.interceptible = false)"),
			sq.Expr("(c.best_if_used_by < NOW())"),
			sq.Expr("(c.build_id IS NULL AND c.resource_config_id IS NULL AND c.worker_resource_cache_id IS NULL)"),
			sq.Expr("(c.resource_config_id IS NOT NULL AND c.worker_base_resource_type_id IS NULL)"),
			sq.Expr("(c.worker_resource_cache_id IS NOT NULL AND v.initialized = true)"),
			sq.Expr("(c.worker_resource_cache_id IS NOT NULL AND rcu.cnt IS NULL)"), // if there are no records, join will add NULL columns
		}).
		ToSql()
	if err != nil {
		return nil, nil, nil, err
	}

	rows, err := factory.conn.Query(query, args...)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()

	creatingContainers := []CreatingContainer{}
	createdContainers := []CreatedContainer{}
	destroyingContainers := []DestroyingContainer{}

	boolShit :=
		psql.Select(
			`
				(c.build_id IS NOT NULL AND b.interceptible = false) OR
				(c.best_if_used_by < NOW()) OR
				(c.build_id IS NULL AND c.resource_config_id IS NULL AND c.worker_resource_cache_id IS NULL) OR
				(c.resource_config_id IS NOT NULL AND c.worker_base_resource_type_id IS NULL) OR
				(c.worker_resource_cache_id IS NOT NULL AND v.initialized = true) OR
				(c.worker_resource_cache_id IS NOT NULL AND rcu.cnt IS NULL)
			`,
			"(c.build_id IS NOT NULL AND b.interceptible = false)",
			"(c.best_if_used_by < NOW())",
			"(c.build_id IS NULL AND c.resource_config_id IS NULL AND c.worker_resource_cache_id IS NULL)",
			"(c.resource_config_id IS NOT NULL AND c.worker_base_resource_type_id IS NULL)",
			"(c.worker_resource_cache_id IS NOT NULL AND v.initialized = true)",
			"(c.worker_resource_cache_id IS NOT NULL AND rcu.cnt IS NULL)", // if there are no records, join will add NULL columns
		).From("containers c").
			LeftJoin("builds b ON b.id = c.build_id").
			LeftJoin("volumes v ON v.worker_resource_cache_id = c.worker_resource_cache_id").
			LeftJoin("worker_resource_caches wrc ON wrc.id = c.worker_resource_cache_id").
			LeftJoin("(select resource_cache_id, count(*) cnt from resource_cache_uses GROUP BY resource_cache_id) rcu ON rcu.resource_cache_id = wrc.resource_cache_id")
	for rows.Next() {
		creatingContainer, createdContainer, destroyingContainer, err := scanContainer(rows, factory.conn)
		if err != nil {
			return nil, nil, nil, err
		}

		var a, b, c, d, e, f, g sql.NullBool
		if creatingContainer != nil {
			err := boolShit.Where(sq.Eq{"c.id": creatingContainer.ID()}).RunWith(factory.conn).QueryRow().Scan(&a, &b, &c, &d, &e, &f, &g)
			if err != nil {
				return nil, nil, nil, err
			}

			log.Println("DESTROY CREATING", creatingContainer.Handle(), a, b, c, d, e, f, g)

			creatingContainers = append(creatingContainers, creatingContainer)
		}

		if createdContainer != nil {
			err := boolShit.Where(sq.Eq{"c.id": createdContainer.ID()}).RunWith(factory.conn).QueryRow().Scan(&a, &b, &c, &d, &e, &f, &g)
			if err != nil {
				return nil, nil, nil, err
			}

			log.Println("DESTROY CREATED", createdContainer.Handle(), a, b, c, d, e, f, g)

			createdContainers = append(createdContainers, createdContainer)
		}

		if destroyingContainer != nil {
			err := boolShit.Where(sq.Eq{"c.id": destroyingContainer.ID()}).RunWith(factory.conn).QueryRow().Scan(&a, &b, &c, &d, &e, &f, &g)
			if err != nil {
				return nil, nil, nil, err
			}

			log.Println("DESTROY DESTROYING", destroyingContainer.Handle(), a, b, c, d, e, f, g)

			destroyingContainers = append(destroyingContainers, destroyingContainer)
		}
	}

	err = rows.Err()
	if err != nil {
		return nil, nil, nil, err
	}

	return creatingContainers, createdContainers, destroyingContainers, nil
}

func selectContainers(asOptional ...string) sq.SelectBuilder {
	columns := []string{"id", "handle", "worker_name", "hijacked", "discontinued", "state"}
	columns = append(columns, containerMetadataColumns...)

	table := "containers"
	if len(asOptional) > 0 {
		as := asOptional[0]

		for i, c := range columns {
			columns[i] = as + "." + c
		}

		table += " " + as
	}

	return psql.Select(columns...).From(table)
}

func scanContainer(row sq.RowScanner, conn Conn) (CreatingContainer, CreatedContainer, DestroyingContainer, error) {
	var (
		id             int
		handle         string
		workerName     string
		isDiscontinued bool
		isHijacked     bool
		state          string

		metadata ContainerMetadata
	)

	columns := []interface{}{&id, &handle, &workerName, &isHijacked, &isDiscontinued, &state}
	columns = append(columns, metadata.ScanTargets()...)

	err := row.Scan(columns...)
	if err != nil {
		return nil, nil, nil, err
	}

	switch state {
	case ContainerStateCreating:
		return newCreatingContainer(
			id,
			handle,
			workerName,
			metadata,
			conn,
		), nil, nil, nil
	case ContainerStateCreated:
		return nil, newCreatedContainer(
			id,
			handle,
			workerName,
			metadata,
			isHijacked,
			conn,
		), nil, nil
	case ContainerStateDestroying:
		return nil, nil, newDestroyingContainer(
			id,
			handle,
			workerName,
			metadata,
			isDiscontinued,
			conn,
		), nil
	}

	return nil, nil, nil, nil
}
