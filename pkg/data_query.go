/*
Copyright 2025 API Testing Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package pkg

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gocql/gocql"

	"github.com/linuxsuren/api-testing/pkg/server"
)

func (s *dbserver) Query(ctx context.Context, query *server.DataQuery) (result *server.DataQueryResult, err error) {
	var db *gocql.Session
	var dbQuery DataQuery
	if dbQuery, err = s.getClientWithDatabase(ctx, query.Key); err != nil {
		return
	}

	db = dbQuery.GetClient()

	result = &server.DataQueryResult{
		Data:  []*server.Pair{},
		Items: make([]*server.Pairs, 0),
		Meta:  &server.DataMeta{},
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		// query database and tables
		if result.Meta.Databases, err = dbQuery.GetDatabases(ctx); err != nil {
			log.Printf("failed to query databases: %v\n", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		if result.Meta.CurrentDatabase = query.Key; query.Key == "" {
			if result.Meta.CurrentDatabase, err = dbQuery.GetCurrentDatabase(); err != nil {
				log.Printf("failed to query current database: %v\n", err)
			}
		}

		if result.Meta.Tables, err = dbQuery.GetTables(ctx, result.Meta.CurrentDatabase); err != nil {
			log.Printf("failed to query tables: %v\n", err)
		}
	}()

	defer wg.Wait()
	// query data
	if query.Sql == "" {
		return
	}

	query.Sql = dbQuery.GetInnerSQL().ToNativeSQL(query.Sql)

	wg.Add(1)
	go func() {
		defer wg.Done()

		result.Meta.Labels = dbQuery.GetLabels(ctx, query.Sql)
		result.Meta.Labels = append(result.Meta.Labels, &server.Pair{
			Key:   "_native_sql",
			Value: query.Sql,
		})
	}()

	var dataResult *server.DataQueryResult
	now := time.Now()
	if dataResult, err = sqlQuery(ctx, query.Sql, db); err == nil {
		result.Items = dataResult.Items
		result.Meta.Duration = time.Since(now).String()
	}
	return
}

func sqlQuery(_ context.Context, sql string, session *gocql.Session) (result *server.DataQueryResult, err error) {
	fmt.Println("query sql", sql)
	iter := session.Query(sql).Iter()
	defer iter.Close()

	result = &server.DataQueryResult{
		Data:  []*server.Pair{},
		Items: make([]*server.Pairs, 0),
		Meta:  &server.DataMeta{},
	}

	for {
		m := make(map[string]any)
		if !iter.MapScan(m) {
			break
		}

		for k, v := range m {
			rowData := &server.Pair{Key: k}

			switch v := v.(type) {
			case []byte:
				rowData.Value = string(v)
			case string:
				rowData.Value = v
			case int, uint64, uint32, int32, int64:
				rowData.Value = fmt.Sprintf("%d", v)
			case float32, float64:
				rowData.Value = fmt.Sprintf("%f", v)
			case time.Time:
				rowData.Value = v.String()
			case bool:
				rowData.Value = fmt.Sprintf("%t", v)
			case nil:
				rowData.Value = "null"
			case []int, []uint64, []uint32, []int32, []int64:
				rowData.Value = fmt.Sprintf("%v", v)
			case []float32, []float64:
				rowData.Value = fmt.Sprintf("%v", v)
			case []string:
				rowData.Value = fmt.Sprintf("%v", v)
			default:
				rowData.Value = fmt.Sprintf("%v", v)
				fmt.Println("column", k, "type", reflect.TypeOf(v))
			}

			result.Data = append(result.Data, rowData)
		}
		result.Items = append(result.Items, &server.Pairs{
			Data: result.Data,
		})
	}
	return
}

const queryDatabaseSql = "show databases"

type DataQuery interface {
	GetDatabases(context.Context) (databases []string, err error)
	GetTables(ctx context.Context, currentDatabase string) (tables []string, err error)
	GetCurrentDatabase() (string, error)
	GetLabels(context.Context, string) []*server.Pair
	GetClient() *gocql.Session
	GetInnerSQL() InnerSQL
}

type commonDataQuery struct {
	session  *gocql.Session
	innerSQL InnerSQL
}

var _ DataQuery = &commonDataQuery{}

func NewCommonDataQuery(innerSQL InnerSQL, session *gocql.Session) DataQuery {
	return &commonDataQuery{
		innerSQL: innerSQL,
		session:  session,
	}
}

func (q *commonDataQuery) GetDatabases(ctx context.Context) (databases []string, err error) {
	var databaseResult *server.DataQueryResult
	if databaseResult, err = sqlQuery(ctx, q.GetInnerSQL().ToNativeSQL(InnerShowDatabases), q.session); err == nil {
		for _, table := range databaseResult.Items {
			for _, item := range table.GetData() {
				if item.Key == "Database" || item.Key == "name" {
					var found bool
					for _, name := range databases {
						if name == item.Value {
							found = true
						}
					}
					if !found {
						databases = append(databases, item.Value)
					}
				}
			}
		}
		sort.Strings(databases)
	}
	return
}

func (q *commonDataQuery) GetTables(ctx context.Context, currentDatabase string) (tables []string, err error) {
	showTables := q.GetInnerSQL().ToNativeSQL(InnerShowTables)
	if strings.Contains(showTables, "%s") {
		showTables = fmt.Sprintf(showTables, currentDatabase)
	}

	var tableResult *server.DataQueryResult
	if tableResult, err = sqlQuery(ctx, showTables, q.session); err == nil {
		for _, table := range tableResult.Items {
			for _, item := range table.GetData() {
				if item.Key == fmt.Sprintf("Tables_in_%s", currentDatabase) || item.Key == "table_name" ||
					item.Key == "Tables" || item.Key == "tablename" || item.Key == "Timeseries" || item.Key == "ChildPaths" {
					var found bool
					for _, name := range tables {
						if name == item.Value {
							found = true
						}
					}
					if !found {
						tables = append(tables, item.Value)
					}
				}
			}
		}
		sort.Strings(tables)
	}
	return
}

func (q *commonDataQuery) GetCurrentDatabase() (current string, err error) {
	var data *server.DataQueryResult
	if data, err = sqlQuery(context.Background(), q.GetInnerSQL().ToNativeSQL(InnerCurrentDB), q.session); err == nil && len(data.Items) > 0 && len(data.Items[0].Data) > 0 {
		current = data.Items[0].Data[0].Value
	}
	return
}

func (q *commonDataQuery) GetLabels(ctx context.Context, sql string) (metadata []*server.Pair) {
	metadata = make([]*server.Pair, 0)
	if databaseResult, err := sqlQuery(ctx, fmt.Sprintf("explain %s", sql), q.session); err == nil && len(databaseResult.Items) == 1 {
		for _, data := range databaseResult.Items[0].Data {
			switch data.Key {
			case "type":
				metadata = append(metadata, &server.Pair{
					Key:   "sql_type",
					Value: data.Value,
				})
			}
		}
	}

	if databaseResult, err := sqlQuery(ctx, "SELECT release_version FROM system.local", q.session); err == nil && len(databaseResult.Items) >= 1 {
		for _, data := range databaseResult.Items[0].Data {
			switch data.Key {
			case "release_version":
				metadata = append(metadata, &server.Pair{
					Key:   "version",
					Value: data.Value,
				})
			}
		}
	}
	return
}

func (q *commonDataQuery) GetClient() *gocql.Session {
	return q.session
}

func (q *commonDataQuery) GetInnerSQL() InnerSQL {
	return q.innerSQL
}
