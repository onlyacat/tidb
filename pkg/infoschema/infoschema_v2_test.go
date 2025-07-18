// Copyright 2024 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package infoschema

import (
	"context"
	"math"
	"testing"

	infoschemacontext "github.com/pingcap/tidb/pkg/infoschema/context"
	"github.com/pingcap/tidb/pkg/infoschema/internal"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/meta"
	"github.com/pingcap/tidb/pkg/meta/autoid"
	"github.com/pingcap/tidb/pkg/meta/model"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/sessionctx/vardef"
	"github.com/stretchr/testify/require"
)

func TestV2Basic(t *testing.T) {
	r := internal.CreateAutoIDRequirement(t)
	defer func() {
		r.Store().Close()
	}()
	is := NewInfoSchemaV2(r, nil, NewData())

	schemaName := ast.NewCIStr("testDB")
	tableName := ast.NewCIStr("test")

	dbInfo := internal.MockDBInfo(t, r.Store(), schemaName.O)
	is.Data.addDB(1, dbInfo)
	internal.AddDB(t, r.Store(), dbInfo)
	tblInfo := internal.MockTableInfo(t, r.Store(), tableName.O)
	tblInfo.DBID = dbInfo.ID
	is.Data.add(tableItem{schemaName, dbInfo.ID, tableName, tblInfo.ID, 2, false}, internal.MockTable(t, r.Store(), tblInfo))
	internal.AddTable(t, r.Store(), dbInfo.ID, tblInfo)
	is.base().schemaMetaVersion = 1
	require.Equal(t, 1, len(is.AllSchemas()))
	ver, err := r.Store().CurrentVersion(kv.GlobalTxnScope)
	require.NoError(t, err)
	is.base().schemaMetaVersion = 2
	is.ts = ver.Ver
	require.Equal(t, 1, len(is.AllSchemas()))
	tblInfos, err := is.SchemaTableInfos(context.Background(), is.AllSchemas()[0].Name)
	require.NoError(t, err)
	require.Equal(t, 1, len(tblInfos))

	getDBInfo, ok := is.SchemaByName(schemaName)
	require.True(t, ok)
	require.Equal(t, dbInfo, getDBInfo)
	require.True(t, is.SchemaExists(schemaName))

	getTableInfo, err := is.TableByName(context.Background(), schemaName, tableName)
	require.NoError(t, err)
	require.NotNil(t, getTableInfo)
	require.True(t, is.TableExists(schemaName, tableName))

	gotTblInfo, err := is.TableInfoByName(schemaName, tableName)
	require.NoError(t, err)
	require.Same(t, gotTblInfo, getTableInfo.Meta())

	gotTblInfo, err = is.TableInfoByName(schemaName, ast.NewCIStr("notexist"))
	require.Error(t, err)
	require.Nil(t, gotTblInfo)

	getDBInfo, ok = is.SchemaByID(dbInfo.ID)
	require.True(t, ok)
	require.Equal(t, dbInfo, getDBInfo)

	getTableInfo, ok = is.TableByID(context.Background(), tblInfo.ID)
	require.True(t, ok)
	require.NotNil(t, getTableInfo)

	gotTblInfo, ok = is.TableInfoByID(tblInfo.ID)
	require.True(t, ok)
	require.Same(t, gotTblInfo, getTableInfo.Meta())

	// negative id should always be seen as not exists
	getTableInfo, ok = is.TableByID(context.Background(), -1)
	require.False(t, ok)
	require.Nil(t, getTableInfo)
	gotTblInfo, ok = is.TableInfoByID(-1)
	require.False(t, ok)
	require.Nil(t, gotTblInfo)
	getDBInfo, ok = is.SchemaByID(-1)
	require.False(t, ok)
	require.Nil(t, getDBInfo)

	gotTblInfo, ok = is.TableInfoByID(1234567)
	require.False(t, ok)
	require.Nil(t, gotTblInfo)

	tables, err := is.SchemaTableInfos(context.Background(), schemaName)
	require.NoError(t, err)
	require.Equal(t, 1, len(tables))
	require.Equal(t, tblInfo.ID, tables[0].ID)

	tblInfos, err1 := is.SchemaTableInfos(context.Background(), schemaName)
	require.NoError(t, err1)
	require.Equal(t, 1, len(tblInfos))
	require.Equal(t, tables[0], tblInfos[0])

	tables, err = is.SchemaTableInfos(context.Background(), ast.NewCIStr("notexist"))
	require.NoError(t, err)
	require.Equal(t, 0, len(tables))

	tblInfos, err = is.SchemaTableInfos(context.Background(), ast.NewCIStr("notexist"))
	require.NoError(t, err)
	require.Equal(t, 0, len(tblInfos))

	require.Equal(t, int64(2), is.SchemaMetaVersion())

	// Test SchemaNameByTableID
	schemaNameByTableIDTests := []struct {
		name       string
		tableID    int64
		wantSchema ast.CIStr
		wantOK     bool
	}{
		{
			name:       "valid table ID",
			tableID:    tblInfo.ID,
			wantSchema: schemaName,
			wantOK:     true,
		},
		{
			name:       "non-existent table ID",
			tableID:    tblInfo.ID + 1,
			wantSchema: ast.CIStr{},
			wantOK:     false,
		},
		{
			name:       "invalid table ID (negative)",
			tableID:    -1,
			wantSchema: ast.CIStr{},
			wantOK:     false,
		},
	}

	for _, tt := range schemaNameByTableIDTests {
		t.Run(tt.name, func(t *testing.T) {
			gotItem, gotOK := is.TableItemByID(tt.tableID)

			require.Equal(t, tt.wantOK, gotOK)
			require.Equal(t, tt.wantSchema, gotItem.DBName)
		})
	}

	// TODO: support FindTableByPartitionID.
}

func TestMisc(t *testing.T) {
	r := internal.CreateAutoIDRequirement(t)
	defer func() {
		r.Store().Close()
	}()

	schemaCacheSize := vardef.SchemaCacheSize.Load()
	builder := NewBuilder(r, schemaCacheSize, nil, NewData(), schemaCacheSize > 0)
	err := builder.InitWithDBInfos(nil, nil, nil, 1)
	require.NoError(t, err)
	is := builder.Build(math.MaxUint64)
	require.Len(t, is.AllResourceGroups(), 0)

	// test create resource group
	resourceGroupInfo := internal.MockResourceGroupInfo(t, r.Store(), "test")
	internal.AddResourceGroup(t, r.Store(), resourceGroupInfo)
	txn, err := r.Store().Begin()
	require.NoError(t, err)
	err = applyCreateOrAlterResourceGroup(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: resourceGroupInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllResourceGroups(), 1)
	getResourceGroupInfo, ok := is.ResourceGroupByName(resourceGroupInfo.Name)
	require.True(t, ok)
	require.Equal(t, resourceGroupInfo, getResourceGroupInfo)
	require.NoError(t, txn.Rollback())

	// create another resource group
	resourceGroupInfo2 := internal.MockResourceGroupInfo(t, r.Store(), "test2")
	internal.AddResourceGroup(t, r.Store(), resourceGroupInfo2)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	err = applyCreateOrAlterResourceGroup(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: resourceGroupInfo2.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllResourceGroups(), 2)
	getResourceGroupInfo, ok = is.ResourceGroupByName(resourceGroupInfo2.Name)
	require.True(t, ok)
	require.Equal(t, resourceGroupInfo2, getResourceGroupInfo)
	require.NoError(t, txn.Rollback())

	// test alter resource group
	resourceGroupInfo.State = model.StatePublic
	internal.UpdateResourceGroup(t, r.Store(), resourceGroupInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	err = applyCreateOrAlterResourceGroup(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: resourceGroupInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllResourceGroups(), 2)
	getResourceGroupInfo, ok = is.ResourceGroupByName(resourceGroupInfo.Name)
	require.True(t, ok)
	require.Equal(t, resourceGroupInfo, getResourceGroupInfo)
	require.NoError(t, txn.Rollback())

	// test drop resource group
	internal.DropResourceGroup(t, r.Store(), resourceGroupInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_ = applyDropResourceGroup(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: resourceGroupInfo.ID})
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllResourceGroups(), 1)
	getResourceGroupInfo, ok = is.ResourceGroupByName(resourceGroupInfo2.Name)
	require.True(t, ok)
	require.Equal(t, resourceGroupInfo2, getResourceGroupInfo)
	require.NoError(t, txn.Rollback())

	// test create policy
	policyInfo := internal.MockPolicyInfo(t, r.Store(), "test")
	internal.CreatePolicy(t, r.Store(), policyInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	err = applyCreatePolicy(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: policyInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllPlacementPolicies(), 1)
	getPolicyInfo, ok := is.PolicyByName(policyInfo.Name)
	require.True(t, ok)
	require.Equal(t, policyInfo, getPolicyInfo)
	require.NoError(t, txn.Rollback())

	// create another policy
	policyInfo2 := internal.MockPolicyInfo(t, r.Store(), "test2")
	internal.CreatePolicy(t, r.Store(), policyInfo2)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	err = applyCreatePolicy(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: policyInfo2.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllPlacementPolicies(), 2)
	getPolicyInfo, ok = is.PolicyByName(policyInfo2.Name)
	require.True(t, ok)
	require.Equal(t, policyInfo2, getPolicyInfo)
	require.NoError(t, txn.Rollback())

	// test alter policy
	policyInfo.State = model.StatePublic
	internal.UpdatePolicy(t, r.Store(), policyInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = applyAlterPolicy(builder, meta.NewMutator(txn), &model.SchemaDiff{SchemaID: policyInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllPlacementPolicies(), 2)
	getPolicyInfo, ok = is.PolicyByName(policyInfo.Name)
	require.True(t, ok)
	require.Equal(t, policyInfo, getPolicyInfo)
	require.NoError(t, txn.Rollback())

	// test drop policy
	internal.DropPolicy(t, r.Store(), policyInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_ = applyDropPolicy(builder, policyInfo.ID)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllPlacementPolicies(), 1)
	getPolicyInfo, ok = is.PolicyByName(policyInfo2.Name)
	require.True(t, ok)
	require.Equal(t, policyInfo2, getPolicyInfo)
	require.NoError(t, txn.Rollback())
}

func TestBundles(t *testing.T) {
	r := internal.CreateAutoIDRequirement(t)
	defer func() {
		r.Store().Close()
	}()

	schemaName := ast.NewCIStr("testDB")
	tableName := ast.NewCIStr("test")
	schemaCacheSize := vardef.SchemaCacheSize.Load()
	builder := NewBuilder(r, schemaCacheSize, nil, NewData(), schemaCacheSize > 0)
	err := builder.InitWithDBInfos(nil, nil, nil, 1)
	require.NoError(t, err)
	is := builder.Build(math.MaxUint64)
	require.Equal(t, 2, len(is.AllSchemas()))

	// create database
	dbInfo := internal.MockDBInfo(t, r.Store(), schemaName.O)
	internal.AddDB(t, r.Store(), dbInfo)
	txn, err := r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateSchema, Version: 1, SchemaID: dbInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Equal(t, 3, len(is.AllSchemas()))
	require.NoError(t, txn.Rollback())

	// create table
	tblInfo := internal.MockTableInfo(t, r.Store(), tableName.O)
	tblInfo.Partition = &model.PartitionInfo{Definitions: []model.PartitionDefinition{{ID: 1}, {ID: 2}}}
	internal.AddTable(t, r.Store(), dbInfo.ID, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateTable, Version: 2, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	tblInfos, err := is.SchemaTableInfos(context.Background(), dbInfo.Name)
	require.NoError(t, err)
	require.Equal(t, 1, len(tblInfos))
	require.NoError(t, txn.Rollback())

	// test create policy
	policyInfo := internal.MockPolicyInfo(t, r.Store(), "test")
	policyInfo.PlacementSettings = &model.PlacementSettings{
		PrimaryRegion: "r1",
		Regions:       "r1,r2",
	}
	internal.CreatePolicy(t, r.Store(), policyInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreatePlacementPolicy, Version: 3, SchemaID: policyInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Len(t, is.AllPlacementPolicies(), 1)
	getPolicyInfo, ok := is.PolicyByName(policyInfo.Name)
	require.True(t, ok)
	require.Equal(t, policyInfo, getPolicyInfo)
	require.NoError(t, txn.Rollback())

	// markTableBundleShouldUpdate
	// test alter table placement
	policyRefInfo := internal.MockPolicyRefInfo(t, r.Store(), "test")
	policyRefInfo.ID = policyInfo.ID
	tblInfo.PlacementPolicyRef = policyRefInfo
	internal.UpdateTable(t, r.Store(), dbInfo, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionAlterTablePlacement, Version: 4, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	getTableInfo, err := is.TableByName(context.Background(), schemaName, tableName)
	require.NoError(t, err)
	require.Equal(t, policyRefInfo, getTableInfo.Meta().PlacementPolicyRef)
	require.NoError(t, txn.Rollback())
	bundle, ok := is.PlacementBundleByPhysicalTableID(tblInfo.ID)
	require.True(t, ok)
	require.Equal(t, bundle.Rules[0].LabelConstraints[0].Values[0], policyInfo.PrimaryRegion)

	// markBundlesReferPolicyShouldUpdate
	// test alter policy
	policyInfo.State = model.StatePublic
	policyInfo.PrimaryRegion = "r2"
	internal.UpdatePolicy(t, r.Store(), policyInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionAlterPlacementPolicy, Version: 5, SchemaID: policyInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	getTableInfo, err = is.TableByName(context.Background(), schemaName, tableName)
	require.NoError(t, err)
	getPolicyInfo, ok = is.PolicyByName(getTableInfo.Meta().PlacementPolicyRef.Name)
	require.True(t, ok)
	require.Equal(t, policyInfo, getPolicyInfo)
	bundle, ok = is.PlacementBundleByPhysicalTableID(tblInfo.ID)
	require.True(t, ok)
	require.Equal(t, bundle.Rules[0].LabelConstraints[0].Values[0], policyInfo.PrimaryRegion)

	// test alter table partition placement
	tblInfo.Partition.Definitions[0].PlacementPolicyRef = policyRefInfo
	internal.UpdateTable(t, r.Store(), dbInfo, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionAlterTablePartitionPlacement, Version: 6, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	bundle, ok = is.PlacementBundleByPhysicalTableID(tblInfo.Partition.Definitions[0].ID)
	require.True(t, ok)
	require.Equal(t, bundle.Rules[0].LabelConstraints[0].Values[0], policyInfo.PrimaryRegion)

	// markPartitionBundleShouldUpdate
	// test alter policy
	policyInfo.PrimaryRegion = "r1"
	internal.UpdatePolicy(t, r.Store(), policyInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionAlterPlacementPolicy, Version: 6, SchemaID: policyInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	bundle, ok = is.PlacementBundleByPhysicalTableID(tblInfo.Partition.Definitions[0].ID)
	require.True(t, ok)
	require.Equal(t, bundle.Rules[0].LabelConstraints[0].Values[0], policyInfo.PrimaryRegion)
}

func TestReferredFKInfo(t *testing.T) {
	r := internal.CreateAutoIDRequirement(t)
	defer func() {
		r.Store().Close()
	}()

	schemaName := ast.NewCIStr("testDB")
	tableName := ast.NewCIStr("testTable")
	schemaCacheSize := vardef.SchemaCacheSize.Load()
	builder := NewBuilder(r, schemaCacheSize, nil, NewData(), schemaCacheSize > 0)
	err := builder.InitWithDBInfos(nil, nil, nil, 1)
	require.NoError(t, err)
	is := builder.Build(math.MaxUint64)
	v2, ok := is.(*infoschemaV2)
	require.True(t, ok)

	// create database
	dbInfo := internal.MockDBInfo(t, r.Store(), schemaName.O)
	internal.AddDB(t, r.Store(), dbInfo)
	txn, err := r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateSchema, Version: 1, SchemaID: dbInfo.ID})
	require.NoError(t, err)

	// check ReferredFKInfo after create table
	tblInfo := internal.MockTableInfo(t, r.Store(), tableName.O)
	tblInfo.ForeignKeys = []*model.FKInfo{{
		ID:        1,
		Name:      ast.NewCIStr("fk_1"),
		RefSchema: ast.NewCIStr("t1"),
		RefTable:  ast.NewCIStr("parent"),
		Version:   1,
	}}
	internal.AddTable(t, r.Store(), dbInfo.ID, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateTable, Version: 2, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	require.Equal(t, v2.Data.referredForeignKeys.Load().Len(), 1)
	ref := v2.GetTableReferredForeignKeys(tblInfo.ForeignKeys[0].RefSchema.L, tblInfo.ForeignKeys[0].RefTable.L)
	require.Equal(t, len(ref), 1)
	require.Equal(t, ref[0].ChildFKName, tblInfo.ForeignKeys[0].Name)

	// check ReferredFKInfo after add foreign key
	tblInfo.ForeignKeys = append(tblInfo.ForeignKeys, &model.FKInfo{
		ID:        2,
		Name:      ast.NewCIStr("fk_2"),
		RefSchema: ast.NewCIStr("t1"),
		RefTable:  ast.NewCIStr("parent"),
		Version:   1,
	})
	internal.UpdateTable(t, r.Store(), dbInfo, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionAddForeignKey, Version: 3, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	require.Equal(t, v2.Data.referredForeignKeys.Load().Len(), 2)
	ref = v2.GetTableReferredForeignKeys(tblInfo.ForeignKeys[0].RefSchema.L, tblInfo.ForeignKeys[0].RefTable.L)
	require.Equal(t, len(ref), 2)
	require.Equal(t, ref[1].ChildFKName, tblInfo.ForeignKeys[1].Name)

	// check ReferredFKInfo after drop foreign key
	tblInfo.ForeignKeys = tblInfo.ForeignKeys[:1]
	internal.UpdateTable(t, r.Store(), dbInfo, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionDropForeignKey, Version: 4, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	require.Equal(t, v2.Data.referredForeignKeys.Load().Len(), 3)
	ref = v2.GetTableReferredForeignKeys(tblInfo.ForeignKeys[0].RefSchema.L, tblInfo.ForeignKeys[0].RefTable.L)
	require.Equal(t, len(ref), 1)
	require.Equal(t, ref[0].ChildFKName, tblInfo.ForeignKeys[0].Name)

	// check ReferredFKInfo after drop table
	internal.DropTable(t, r.Store(), dbInfo, tblInfo.ID, tblInfo.Name.L)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionDropTable, Version: 5, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	require.Equal(t, v2.Data.referredForeignKeys.Load().Len(), 4)
	ref = v2.GetTableReferredForeignKeys(tblInfo.ForeignKeys[0].RefSchema.L, tblInfo.ForeignKeys[0].RefTable.L)
	require.Equal(t, len(ref), 0)
}

func updateTableSpecialAttribute(t *testing.T, dbInfo *model.DBInfo, tblInfo *model.TableInfo, builder *Builder, r autoid.Requirement,
	actionType model.ActionType, ver int64, filter infoschemacontext.SpecialAttributeFilter, add bool) *model.TableInfo {
	internal.UpdateTable(t, r.Store(), dbInfo, tblInfo)
	txn, err := r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: actionType, Version: ver, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	is := builder.Build(math.MaxUint64)
	tblInfoRes := is.ListTablesWithSpecialAttribute(filter)
	if add {
		// add special attribute
		require.Equal(t, 1, len(tblInfoRes))
		require.Equal(t, 1, len(tblInfoRes[0].TableInfos))
		return tblInfoRes[0].TableInfos[0]
	}
	require.Equal(t, 0, len(tblInfoRes))
	return nil
}

func TestSpecialAttributeCorrectnessInSchemaChange(t *testing.T) {
	r := internal.CreateAutoIDRequirement(t)
	defer func() {
		r.Store().Close()
	}()

	schemaName := ast.NewCIStr("testDB")
	tableName := ast.NewCIStr("testTable")
	schemaCacheSize := vardef.SchemaCacheSize.Load()
	builder := NewBuilder(r, schemaCacheSize, nil, NewData(), schemaCacheSize > 0)
	err := builder.InitWithDBInfos(nil, nil, nil, 1)
	require.NoError(t, err)
	is := builder.Build(math.MaxUint64)
	require.Equal(t, 2, len(is.AllSchemas()))

	// create database
	dbInfo := internal.MockDBInfo(t, r.Store(), schemaName.O)
	internal.AddDB(t, r.Store(), dbInfo)
	txn, err := r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateSchema, Version: 1, SchemaID: dbInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	require.Equal(t, 3, len(is.AllSchemas()))
	require.NoError(t, txn.Rollback())

	// create table
	tblInfo := internal.MockTableInfo(t, r.Store(), tableName.O)
	internal.AddTable(t, r.Store(), dbInfo.ID, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateTable, Version: 2, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	is = builder.Build(math.MaxUint64)
	tblInfos, err := is.SchemaTableInfos(context.Background(), dbInfo.Name)
	require.NoError(t, err)
	require.Equal(t, 1, len(tblInfos))
	require.NoError(t, txn.Rollback())

	// tests partition info correctness in schema change
	tblInfo.Partition = &model.PartitionInfo{
		Expr: "aa+1",
		Columns: []ast.CIStr{
			ast.NewCIStr("aa"),
		},
		Definitions: []model.PartitionDefinition{
			{ID: 1, Name: ast.NewCIStr("p1")},
			{ID: 2, Name: ast.NewCIStr("p2")},
		},
		Enable:   true,
		DDLState: model.StatePublic,
	}
	// add partition
	tblInfo1 := updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionAddTablePartition, 3, infoschemacontext.PartitionAttribute, true)
	require.Equal(t, tblInfo.Partition, tblInfo1.Partition)
	// drop partition
	tblInfo.Partition.Definitions = tblInfo.Partition.Definitions[:1]
	tblInfo1 = updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionDropTablePartition, 4, infoschemacontext.PartitionAttribute, true)
	require.Equal(t, tblInfo.Partition, tblInfo1.Partition)

	// test placement policy correctness in schema change
	tblInfo.PlacementPolicyRef = &model.PolicyRefInfo{
		ID:   1,
		Name: ast.NewCIStr("p3"),
	}
	tblInfo1 = updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionAlterTablePlacement, 5, infoschemacontext.PlacementPolicyAttribute, true)
	require.Equal(t, tblInfo.PlacementPolicyRef, tblInfo1.PlacementPolicyRef)
	tblInfo.PlacementPolicyRef = nil
	updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionAlterTablePlacement, 6, infoschemacontext.PlacementPolicyAttribute, false)

	// test tiflash replica correctness in schema change
	tblInfo.TiFlashReplica = &model.TiFlashReplicaInfo{
		Count:          1,
		Available:      true,
		LocationLabels: []string{"zone"},
	}
	tblInfo1 = updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionSetTiFlashReplica, 7, infoschemacontext.TiFlashAttribute, true)
	require.Equal(t, tblInfo.TiFlashReplica, tblInfo1.TiFlashReplica)
	tblInfo.TiFlashReplica = nil
	updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionSetTiFlashReplica, 8, infoschemacontext.TiFlashAttribute, false)

	// test table lock correctness in schema change
	tblInfo.Lock = &model.TableLockInfo{
		Tp:    ast.TableLockRead,
		State: model.TableLockStatePublic,
		TS:    1,
	}
	tblInfo1 = updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionLockTable, 9, infoschemacontext.TableLockAttribute, true)
	require.Equal(t, tblInfo.Lock, tblInfo1.Lock)
	tblInfo.Lock = nil
	updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionUnlockTable, 10, infoschemacontext.TableLockAttribute, false)
}

func TestDataStructFieldsCorrectnessInSchemaChange(t *testing.T) {
	r := internal.CreateAutoIDRequirement(t)
	defer func() {
		r.Store().Close()
	}()

	schemaName := ast.NewCIStr("testDB")
	tableName := ast.NewCIStr("testTable")
	schemaCacheSize := vardef.SchemaCacheSize.Load()
	builder := NewBuilder(r, schemaCacheSize, nil, NewData(), schemaCacheSize > 0)
	err := builder.InitWithDBInfos(nil, nil, nil, 1)
	require.NoError(t, err)
	is := builder.Build(math.MaxUint64)
	v2, ok := is.(*infoschemaV2)
	require.True(t, ok)

	// verify schema related fields after create database
	dbInfo := internal.MockDBInfo(t, r.Store(), schemaName.O)
	internal.AddDB(t, r.Store(), dbInfo)
	txn, err := r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateSchema, Version: 1, SchemaID: dbInfo.ID})
	require.NoError(t, err)
	dbIDName, ok := v2.Data.schemaID2Name.Load().Get(schemaIDName{id: dbInfo.ID, schemaVersion: 1})
	require.True(t, ok)
	require.Equal(t, dbIDName.name, dbInfo.Name)
	dbItem, ok := v2.Data.schemaMap.Load().Get(schemaItem{schemaVersion: 1, dbInfo: &model.DBInfo{Name: dbInfo.Name}})
	require.True(t, ok)
	require.Equal(t, dbItem.dbInfo.ID, dbInfo.ID)

	// verify table related fields after create table
	tblInfo := internal.MockTableInfo(t, r.Store(), tableName.O)
	internal.AddTable(t, r.Store(), dbInfo.ID, tblInfo)
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionCreateTable, Version: 2, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	tblItem, ok := v2.Data.byName.Load().Get(&tableItem{dbName: dbInfo.Name, tableName: tblInfo.Name, schemaVersion: 2})
	require.True(t, ok)
	require.Equal(t, tblItem.tableID, tblInfo.ID)
	tblItem, ok = v2.Data.byID.Load().Get(&tableItem{tableID: tblInfo.ID, schemaVersion: 2})
	require.True(t, ok)
	require.Equal(t, tblItem.dbID, dbInfo.ID)
	tbl, ok := v2.Data.tableCache.Get(tableCacheKey{tableID: tblInfo.ID, schemaVersion: 2})
	require.True(t, ok)
	require.Equal(t, tbl.Meta().Name, tblInfo.Name)

	// verify partition related fields after add partition
	require.Equal(t, v2.Data.pid2tid.Load().Len(), 0)
	tblInfo.Partition = &model.PartitionInfo{
		Definitions: []model.PartitionDefinition{
			{ID: 1, Name: ast.NewCIStr("p1")},
			{ID: 2, Name: ast.NewCIStr("p2")},
		},
		Enable:   true,
		DDLState: model.StatePublic,
	}
	tblInfo1 := updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionAddTablePartition, 3, infoschemacontext.PartitionAttribute, true)
	require.Equal(t, tblInfo.Partition, tblInfo1.Partition)
	require.Equal(t, v2.Data.pid2tid.Load().Len(), 2)
	tblInfoItem, ok := v2.Data.pid2tid.Load().Get(partitionItem{partitionID: 2, schemaVersion: 3})
	require.True(t, ok)
	require.Equal(t, tblInfoItem.tableID, tblInfo.ID)

	// verify partition related fields drop partition
	tblInfo.Partition.Definitions = tblInfo.Partition.Definitions[:1]
	tblInfo1 = updateTableSpecialAttribute(t, dbInfo, tblInfo, builder, r, model.ActionDropTablePartition, 4, infoschemacontext.PartitionAttribute, true)
	require.Equal(t, tblInfo.Partition, tblInfo1.Partition)
	require.Equal(t, v2.Data.pid2tid.Load().Len(), 4)
	tblInfoItem, ok = v2.Data.pid2tid.Load().Get(partitionItem{partitionID: 1, schemaVersion: 4})
	require.True(t, ok)
	require.False(t, tblInfoItem.tomb)
	tblInfoItem, ok = v2.Data.pid2tid.Load().Get(partitionItem{partitionID: 2, schemaVersion: 4})
	require.True(t, ok)
	require.True(t, tblInfoItem.tomb)

	// verify table and partition related fields after drop table
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	m := meta.NewMutator(txn)
	_, err = builder.ApplyDiff(m, &model.SchemaDiff{Type: model.ActionDropTable, Version: 5, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	// at first, the table will not be removed
	tblItem, ok = v2.Data.byName.Load().Get(&tableItem{dbName: dbInfo.Name, tableName: tblInfo.Name, schemaVersion: 5})
	require.True(t, ok)
	require.False(t, tblItem.tomb)
	tblItem, ok = v2.Data.byID.Load().Get(&tableItem{tableID: tblInfo.ID, schemaVersion: 5})
	require.True(t, ok)
	require.False(t, tblItem.tomb)
	_, ok = v2.Data.tableCache.Get(tableCacheKey{tableID: tblInfo.ID, schemaVersion: 5})
	require.True(t, ok)
	require.Equal(t, v2.Data.pid2tid.Load().Len(), 5) // tomb partition info
	tblInfoItem, ok = v2.Data.pid2tid.Load().Get(partitionItem{partitionID: 1, schemaVersion: 5})
	require.True(t, ok)
	require.False(t, tblInfoItem.tomb)
	// after actually drop the table, the info will be tomb
	m.DropTableOrView(dbInfo.ID, tblInfo.ID)
	_, err = builder.ApplyDiff(m, &model.SchemaDiff{Type: model.ActionDropTable, Version: 5, SchemaID: dbInfo.ID, TableID: tblInfo.ID})
	require.NoError(t, err)
	// at first, the table will not be removed
	tblItem, ok = v2.Data.byName.Load().Get(&tableItem{dbName: dbInfo.Name, tableName: tblInfo.Name, schemaVersion: 5})
	require.True(t, ok)
	require.True(t, tblItem.tomb)
	tblItem, ok = v2.Data.byID.Load().Get(&tableItem{tableID: tblInfo.ID, schemaVersion: 5})
	require.True(t, ok)
	require.True(t, tblItem.tomb)
	_, ok = v2.Data.tableCache.Get(tableCacheKey{tableID: tblInfo.ID, schemaVersion: 5})
	require.False(t, ok)
	require.Equal(t, v2.Data.pid2tid.Load().Len(), 5) // tomb partition info
	tblInfoItem, ok = v2.Data.pid2tid.Load().Get(partitionItem{partitionID: 1, schemaVersion: 5})
	require.True(t, ok)
	require.True(t, tblInfoItem.tomb)

	// verify schema related fields after drop database
	txn, err = r.Store().Begin()
	require.NoError(t, err)
	_, err = builder.ApplyDiff(meta.NewMutator(txn), &model.SchemaDiff{Type: model.ActionDropSchema, Version: 6, SchemaID: dbInfo.ID})
	require.NoError(t, err)
	dbIDName, ok = v2.Data.schemaID2Name.Load().Get(schemaIDName{id: dbInfo.ID, schemaVersion: 6})
	require.True(t, ok)
	require.True(t, dbIDName.tomb)
	dbItem, ok = v2.Data.schemaMap.Load().Get(schemaItem{schemaVersion: 6, dbInfo: &model.DBInfo{Name: dbInfo.Name}})
	require.True(t, ok)
	require.True(t, dbItem.tomb)
}

func TestReferredForeignKeys(t *testing.T) {
	data := NewData()

	// schema1.table1 with two FKs
	tbl1 := &model.TableInfo{
		Name: ast.NewCIStr("table1"),
		ForeignKeys: []*model.FKInfo{
			{Name: ast.NewCIStr("fk1"), RefSchema: ast.NewCIStr("db1"), RefTable: ast.NewCIStr("table1"), Version: model.FKVersion1},
			{Name: ast.NewCIStr("fk2"), RefSchema: ast.NewCIStr("db1"), RefTable: ast.NewCIStr("table1"), Version: model.FKVersion1},
		},
	}
	data.addReferredForeignKeys(ast.NewCIStr("db1"), tbl1, 1)
	got1 := data.getTableReferredForeignKeys("db1", "table1", 1)
	expected1 := []*model.ReferredFKInfo{
		{ChildSchema: ast.NewCIStr("db1"), ChildTable: ast.NewCIStr("table1"), ChildFKName: ast.NewCIStr("fk1")},
		{ChildSchema: ast.NewCIStr("db1"), ChildTable: ast.NewCIStr("table1"), ChildFKName: ast.NewCIStr("fk2")},
	}
	require.Equal(t, expected1, got1)

	// schema1.table2 has none
	require.Empty(t, data.getTableReferredForeignKeys("db1", "table2", 1))

	// schema2.tableA with one FK
	tblA := &model.TableInfo{
		Name: ast.NewCIStr("tableA"),
		ForeignKeys: []*model.FKInfo{
			{Name: ast.NewCIStr("fkA"), RefSchema: ast.NewCIStr("db2"), RefTable: ast.NewCIStr("tableA"), Version: model.FKVersion1},
		},
	}
	data.addReferredForeignKeys(ast.NewCIStr("db2"), tblA, 2)
	got2 := data.getTableReferredForeignKeys("db2", "tablea", 2)
	expected2 := []*model.ReferredFKInfo{
		{ChildSchema: ast.NewCIStr("db2"), ChildTable: ast.NewCIStr("tableA"), ChildFKName: ast.NewCIStr("fkA")},
	}
	require.Equal(t, expected2, got2)

	// cross-schema/table lookups return empty
	require.Empty(t, data.getTableReferredForeignKeys("db1", "tablea", 2))
	require.Empty(t, data.getTableReferredForeignKeys("db2", "table1", 2))

	// delete all FKs for db1.table1
	data.deleteReferredForeignKeys(ast.NewCIStr("db1"), tbl1, 3)
	require.Empty(t, data.getTableReferredForeignKeys("db1", "table1", 3))
	require.Equal(t, expected2, data.getTableReferredForeignKeys("db2", "tablea", 3))

	// delete the FK for db2.tableA
	data.deleteReferredForeignKeys(ast.NewCIStr("db2"), tblA, 4)
	require.Empty(t, data.getTableReferredForeignKeys("db2", "tablea", 4))

	// get with old version
	require.Equal(t, expected1, data.getTableReferredForeignKeys("db1", "table1", 2))
	require.Equal(t, expected2, data.getTableReferredForeignKeys("db2", "tablea", 2))
}

func TestGCOldFKVersion(t *testing.T) {
	data := NewData()
	// helper to make items
	mk := func(db, tbl, cs, ct, cf string, ver int64, tomb bool) *referredForeignKeyItem {
		return &referredForeignKeyItem{
			dbName:        db,
			tableName:     tbl,
			schemaVersion: ver,
			tomb:          tomb,
			referredFKInfo: []*model.ReferredFKInfo{{
				ChildSchema: ast.NewCIStr(cs),
				ChildTable:  ast.NewCIStr(ct),
				ChildFKName: ast.NewCIStr(cf),
			}},
		}
	}
	// prepare two groups: db1.table1.fkX and db2.table2.fkY
	items := []*referredForeignKeyItem{
		mk("db1", "table1", "s", "t", "fk", 5, false),
		mk("db1", "table1", "s", "t", "fk", 4, true),
		mk("db1", "table1", "s", "t", "fk", 3, false),
		mk("db1", "table1", "s", "t", "fk", 2, true),
		mk("db1", "table1", "s", "t", "fk", 1, false),
		mk("db2", "table2", "s", "t", "fk", 1, false), // unaffected
	}
	for _, it := range items {
		btreeSet(&data.referredForeignKeys, it)
	}
	before := data.referredForeignKeys.Load().Len()
	require.Equal(t, 6, before)

	// GC entries older than version 4
	deleted := data.gcOldFKVersion(4)
	require.Equal(t, 3, deleted) // versions 3,2,1 for group1
	after := data.referredForeignKeys.Load().Len()
	require.Equal(t, 3, after) // kept version 5 & 4 for group1, and the single group2

	// verify surviving versions
	var vers []int64
	data.referredForeignKeys.Load().Ascend(func(item *referredForeignKeyItem) bool {
		vers = append(vers, item.schemaVersion)
		return true
	})
	require.Equal(t, []int64{4, 5, 1}, vers)

	// ensure getTableReferredForeignKeys respects GC boundary
	got := data.getTableReferredForeignKeys("db1", "table1", 5)
	require.Equal(t, &model.ReferredFKInfo{
		ChildSchema: ast.NewCIStr("s"),
		ChildTable:  ast.NewCIStr("t"),
		ChildFKName: ast.NewCIStr("fk"),
	}, got[0])
}
