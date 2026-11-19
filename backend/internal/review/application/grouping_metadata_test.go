package application

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestObjectsAtStatementOrdinalCopiesWithoutMutatingResult(t *testing.T) {
	source := []fingerprint.ObjectReference{
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationDefine},
		{Token: "column_1", Kind: "column", Relation: fingerprint.RelationDefine, ParentToken: "table_1"},
	}

	first := objectsAtStatementOrdinal(source, 1)
	second := objectsAtStatementOrdinal(source, 3)
	if source[0].StatementOrdinal != 0 || source[1].StatementOrdinal != 0 {
		t.Fatalf("fingerprint Result objects were mutated: %+v", source)
	}
	for _, object := range first {
		if object.StatementOrdinal != 1 {
			t.Fatalf("ordinal 1 object = %+v", object)
		}
		want := []fingerprint.StatementOrdinalRange{{Start: 1, End: 1}}
		if !reflect.DeepEqual(object.StatementOrdinalRanges, want) {
			t.Fatalf("ordinal 1 ranges = %+v, want %+v", object.StatementOrdinalRanges, want)
		}
	}
	for _, object := range second {
		if object.StatementOrdinal != 3 {
			t.Fatalf("ordinal 3 object = %+v", object)
		}
		want := []fingerprint.StatementOrdinalRange{{Start: 3, End: 3}}
		if !reflect.DeepEqual(object.StatementOrdinalRanges, want) {
			t.Fatalf("ordinal 3 ranges = %+v, want %+v", object.StatementOrdinalRanges, want)
		}
	}
}

func TestMergeObjectsRetainsDistinctReferencesAndFirstOrdinal(t *testing.T) {
	state := newObjectMergeState()
	budget := &objectContextBudget{}
	objects, complete := mergeObjects(nil, state, []fingerprint.ObjectReference{
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationRead, StatementOrdinal: 1},
		{Token: "column_1", Kind: "column", Relation: fingerprint.RelationReference, ParentToken: "table_1", StatementOrdinal: 1},
	}, budget)
	if !complete {
		t.Fatal("initial objects unexpectedly exceeded a context budget")
	}
	objects, complete = mergeObjects(objects, state, []fingerprint.ObjectReference{
		{Token: "table_2", Kind: "table", Relation: fingerprint.RelationRead, StatementOrdinal: 2},
		{Token: "column_1", Kind: "column", Relation: fingerprint.RelationReference, ParentToken: "table_1", StatementOrdinal: 2},
	}, budget)
	if !complete {
		t.Fatal("objects within every budget reported incomplete")
	}

	if len(objects) != 3 {
		t.Fatalf("merged objects = %+v, want 3 distinct references", objects)
	}
	if objects[0].Token != "table_1" || objects[0].StatementOrdinal != 1 {
		t.Fatalf("first table changed: %+v", objects[0])
	}
	if objects[1].Token != "column_1" || objects[1].StatementOrdinal != 1 {
		t.Fatalf("duplicate column must retain first ordinal: %+v", objects[1])
	}
	wantColumnRanges := []fingerprint.StatementOrdinalRange{{Start: 1, End: 2}}
	if !reflect.DeepEqual(objects[1].StatementOrdinalRanges, wantColumnRanges) {
		t.Fatalf("duplicate column ranges = %+v, want %+v",
			objects[1].StatementOrdinalRanges, wantColumnRanges)
	}
	if objects[2].Token != "table_2" || objects[2].StatementOrdinal != 2 {
		t.Fatalf("second table was collapsed: %+v", objects[2])
	}
}

func TestMergeObjectsSeparatesRelationAndSchemaIdentity(t *testing.T) {
	objects, complete := mergeObjects(nil, newObjectMergeState(), []fingerprint.ObjectReference{
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationRead, SchemaToken: "schema_1", StatementOrdinal: 1},
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationWrite, SchemaToken: "schema_1", StatementOrdinal: 1},
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationRead, SchemaToken: "schema_2", StatementOrdinal: 1},
	}, &objectContextBudget{})
	if !complete {
		t.Fatal("distinct relation/schema references unexpectedly exceeded a context budget")
	}
	if len(objects) != 3 {
		t.Fatalf("relation/schema variants collapsed: %+v", objects)
	}
}

func TestMergeObjectsTracksEachObjectsOccurrences(t *testing.T) {
	objects, complete := mergeObjects(nil, newObjectMergeState(), []fingerprint.ObjectReference{
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationTarget, StatementOrdinal: 1},
		{Token: "table_2", Kind: "table", Relation: fingerprint.RelationTarget, StatementOrdinal: 3},
		{Token: "table_1", Kind: "table", Relation: fingerprint.RelationTarget, StatementOrdinal: 5},
	}, &objectContextBudget{})
	if !complete {
		t.Fatal("three object occurrences unexpectedly exceeded a context budget")
	}
	if len(objects) != 2 {
		t.Fatalf("merged objects = %+v, want two distinct tables", objects)
	}
	wantTable1 := []fingerprint.StatementOrdinalRange{{Start: 1, End: 1}, {Start: 5, End: 5}}
	if objects[0].Token != "table_1" || objects[0].StatementOrdinal != 1 ||
		!reflect.DeepEqual(objects[0].StatementOrdinalRanges, wantTable1) {
		t.Fatalf("table_1 occurrences = %+v, want ordinal 1 and ranges %+v",
			objects[0], wantTable1)
	}
	wantTable2 := []fingerprint.StatementOrdinalRange{{Start: 3, End: 3}}
	if objects[1].Token != "table_2" ||
		!reflect.DeepEqual(objects[1].StatementOrdinalRanges, wantTable2) {
		t.Fatalf("table_2 occurrences = %+v, want ranges %+v", objects[1], wantTable2)
	}
}

func TestObjectOccurrenceRangesReachProviderDTO(t *testing.T) {
	group := provider.AnonymizedGroup{
		GroupID:                     "g-1",
		RepresentativeAnonymizedSQL: "update table_1 set column_1 = :int where column_2 = :int",
		MetadataContextComplete:     true,
		StatementCount:              3,
		StatementOrdinal:            1,
		StatementOrdinalRanges:      []provider.StatementOrdinalRange{{Start: 1, End: 3}},
		Objects: []provider.AnonymizedObjectReference{{
			Token:            "table_1",
			Kind:             "table",
			Relation:         "target",
			StatementOrdinal: 1,
			StatementOrdinalRanges: []provider.StatementOrdinalRange{
				{Start: 1, End: 1},
				{Start: 3, End: 3},
			},
		}},
	}
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Objects []struct {
			StatementOrdinal       int32                            `json:"statement_ordinal"`
			StatementOrdinalRanges []provider.StatementOrdinalRange `json:"statement_ordinal_ranges"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Objects) != 1 || decoded.Objects[0].StatementOrdinal != 1 {
		t.Fatalf("provider object representative ordinal was lost: %s", raw)
	}
	want := []provider.StatementOrdinalRange{{Start: 1, End: 1}, {Start: 3, End: 3}}
	if !reflect.DeepEqual(decoded.Objects[0].StatementOrdinalRanges, want) {
		t.Fatalf("provider object ranges = %+v, want %+v", decoded.Objects[0].StatementOrdinalRanges, want)
	}
}

func TestMergeMetadataContextCompleteUsesAND(t *testing.T) {
	got := mergeMetadataContextComplete(true, true)
	if !got {
		t.Fatal("two complete contexts must remain complete")
	}
	got = mergeMetadataContextComplete(got, false)
	if got {
		t.Fatal("one incomplete context must make the group incomplete")
	}
	if mergeMetadataContextComplete(got, true) {
		t.Fatal("group completeness must not recover after an incomplete statement")
	}
}

func TestStatementOrdinalRangesKeepInterleavedGroupsSeparate(t *testing.T) {
	var alpha, beta []statementOrdinalRange
	for ordinal := int32(1); ordinal <= 6; ordinal++ {
		var ok bool
		if ordinal%2 == 1 {
			alpha, ok = appendStatementOrdinalRange(alpha, ordinal)
		} else {
			beta, ok = appendStatementOrdinalRange(beta, ordinal)
		}
		if !ok {
			t.Fatalf("ordinal %d unexpectedly exceeded range limit", ordinal)
		}
	}
	wantAlpha := []statementOrdinalRange{{Start: 1, End: 1}, {Start: 3, End: 3}, {Start: 5, End: 5}}
	wantBeta := []statementOrdinalRange{{Start: 2, End: 2}, {Start: 4, End: 4}, {Start: 6, End: 6}}
	if !reflect.DeepEqual(alpha, wantAlpha) {
		t.Fatalf("alpha ranges = %+v, want %+v", alpha, wantAlpha)
	}
	if !reflect.DeepEqual(beta, wantBeta) {
		t.Fatalf("beta ranges = %+v, want %+v", beta, wantBeta)
	}
}

func TestStatementOrdinalRangesMergeAdjacentOrdinals(t *testing.T) {
	var ranges []statementOrdinalRange
	for _, ordinal := range []int32{1, 2, 3, 5, 6, 8} {
		var ok bool
		ranges, ok = appendStatementOrdinalRange(ranges, ordinal)
		if !ok {
			t.Fatalf("ordinal %d unexpectedly exceeded range limit", ordinal)
		}
	}
	want := []statementOrdinalRange{{Start: 1, End: 3}, {Start: 5, End: 6}, {Start: 8, End: 8}}
	if !reflect.DeepEqual(ranges, want) {
		t.Fatalf("ranges = %+v, want %+v", ranges, want)
	}
}

func TestStatementOrdinalRangesStopAt512AndReportIncomplete(t *testing.T) {
	var ranges []statementOrdinalRange
	for i := 0; i < maxStatementOrdinalRanges; i++ {
		ordinal := int32(2*i + 1)
		var ok bool
		ranges, ok = appendStatementOrdinalRange(ranges, ordinal)
		if !ok {
			t.Fatalf("range %d unexpectedly rejected", i+1)
		}
	}
	if len(ranges) != maxStatementOrdinalRanges {
		t.Fatalf("range count = %d, want %d", len(ranges), maxStatementOrdinalRanges)
	}

	// Adjacent to the final range does not consume another slot.
	var ok bool
	ranges, ok = appendStatementOrdinalRange(ranges, int32(2*maxStatementOrdinalRanges))
	if !ok || len(ranges) != maxStatementOrdinalRanges {
		t.Fatalf("adjacent extension must stay within limit: ok=%v ranges=%d", ok, len(ranges))
	}

	ranges, ok = appendStatementOrdinalRange(ranges, int32(2*maxStatementOrdinalRanges+2))
	if ok {
		t.Fatal("513th disjoint range must report incomplete metadata context")
	}
	if len(ranges) != maxStatementOrdinalRanges {
		t.Fatalf("overflow changed bounded range count: %d", len(ranges))
	}
}

func TestMergeObjectsStopsAtPerObjectRangeLimit(t *testing.T) {
	state := newObjectMergeState()
	budget := &objectContextBudget{}
	var objects []fingerprint.ObjectReference
	for i := 0; i < maxObjectOrdinalRanges; i++ {
		objects, _ = mergeObjects(objects, state, []fingerprint.ObjectReference{{
			Token:            "table_1",
			Kind:             "table",
			Relation:         fingerprint.RelationTarget,
			StatementOrdinal: int32(2*i + 1),
		}}, budget)
	}
	if len(objects) != 1 || len(objects[0].StatementOrdinalRanges) != maxObjectOrdinalRanges {
		t.Fatalf("bounded object ranges = %d on %d objects", len(objects[0].StatementOrdinalRanges), len(objects))
	}
	objects, complete := mergeObjects(objects, state, []fingerprint.ObjectReference{{
		Token:            "table_1",
		Kind:             "table",
		Relation:         fingerprint.RelationTarget,
		StatementOrdinal: int32(2*maxObjectOrdinalRanges + 1),
	}}, budget)
	if complete {
		t.Fatal("513th object range must report incomplete metadata context")
	}
	if len(objects[0].StatementOrdinalRanges) != maxObjectOrdinalRanges {
		t.Fatalf("range overflow changed bounded count: %d", len(objects[0].StatementOrdinalRanges))
	}
}

func TestMergeObjectsStopsAtPerGroupObjectLimit(t *testing.T) {
	state := newObjectMergeState()
	objects, complete := mergeObjects(nil, state, distinctObjectRefs(0, maxObjectReferencesPerGroup), &objectContextBudget{})
	if !complete {
		t.Fatal("exact per-group object limit must be accepted")
	}
	objects, complete = mergeObjects(objects, state, distinctObjectRefs(maxObjectReferencesPerGroup, 1), &objectContextBudget{})
	if complete {
		t.Fatal("object over per-group limit must report incomplete metadata context")
	}
	if len(objects) != maxObjectReferencesPerGroup {
		t.Fatalf("per-group object overflow changed bounded count: %d", len(objects))
	}
}

func TestMergeObjectsStopsAtPerGroupRangeMembershipLimit(t *testing.T) {
	state := newObjectMergeState()
	budget := &objectContextBudget{}
	var objects []fingerprint.ObjectReference
	for occurrence := int32(1); occurrence <= maxObjectOrdinalRangesPerGroup; occurrence++ {
		ref := fingerprint.ObjectReference{
			Token:            "table_" + strconv.Itoa(int((occurrence-1)/maxObjectOrdinalRanges+1)),
			Kind:             "table",
			Relation:         fingerprint.RelationTarget,
			StatementOrdinal: 2*occurrence - 1,
		}
		var complete bool
		objects, complete = mergeObjects(objects, state, []fingerprint.ObjectReference{ref}, budget)
		if !complete {
			t.Fatalf("range membership %d unexpectedly exceeded the group budget", occurrence)
		}
	}
	if state.ranges != maxObjectOrdinalRangesPerGroup {
		t.Fatalf("group range memberships = %d, want %d", state.ranges, maxObjectOrdinalRangesPerGroup)
	}
	objects, complete := mergeObjects(objects, state, []fingerprint.ObjectReference{{
		Token: "table_extra", Kind: "table", Relation: fingerprint.RelationTarget, StatementOrdinal: maxObjectOrdinalRangesPerGroup + 1,
	}}, budget)
	if complete {
		t.Fatal("range membership over per-group limit must report incomplete metadata context")
	}
	if state.ranges != maxObjectOrdinalRangesPerGroup {
		t.Fatalf("group range overflow changed bounded count: %d", state.ranges)
	}
}

func TestObjectContextRunBudgetIsSharedAcrossGroups(t *testing.T) {
	runBudget := &objectContextBudget{}
	for group := 0; group < maxObjectReferencesPerRun/maxObjectReferencesPerGroup; group++ {
		start := group * maxObjectReferencesPerGroup
		objects, complete := mergeObjects(nil, newObjectMergeState(),
			distinctObjectRefs(start, maxObjectReferencesPerGroup), runBudget)
		if !complete || len(objects) != maxObjectReferencesPerGroup {
			t.Fatalf("group %d within run object budget: complete=%v objects=%d",
				group, complete, len(objects))
		}
	}
	objects, complete := mergeObjects(nil, newObjectMergeState(),
		distinctObjectRefs(maxObjectReferencesPerRun, 1), runBudget)
	if complete {
		t.Fatal("object over run budget must report incomplete metadata context")
	}
	if len(objects) != 0 || runBudget.objects != maxObjectReferencesPerRun {
		t.Fatalf("run object overflow changed bounded state: objects=%d budget=%+v",
			len(objects), runBudget)
	}
}

func TestObjectContextRunRangeBudgetIsSharedAcrossGroups(t *testing.T) {
	runBudget := &objectContextBudget{}
	rangesPerObject := maxObjectOrdinalRangesPerRun / maxObjectReferencesPerRun
	for group := 0; group < maxObjectReferencesPerRun/maxObjectReferencesPerGroup; group++ {
		state := newObjectMergeState()
		start := group * maxObjectReferencesPerGroup
		var objects []fingerprint.ObjectReference
		for occurrence := int32(1); occurrence <= int32(rangesPerObject); occurrence++ {
			var complete bool
			objects, complete = mergeObjects(objects, state,
				distinctObjectRefsAtOrdinal(start, maxObjectReferencesPerGroup, 2*occurrence-1), runBudget)
			if !complete {
				t.Fatalf("group %d range %d unexpectedly exceeded the run budget", group, occurrence)
			}
		}
	}
	if runBudget.ranges != maxObjectOrdinalRangesPerRun {
		t.Fatalf("run range memberships = %d, want %d",
			runBudget.ranges, maxObjectOrdinalRangesPerRun)
	}
	objects, complete := mergeObjects(nil, newObjectMergeState(),
		distinctObjectRefsAtOrdinal(maxObjectReferencesPerRun, 1, 1), runBudget)
	if complete {
		t.Fatal("range over run budget must report incomplete metadata context")
	}
	if len(objects) != 0 || runBudget.ranges != maxObjectOrdinalRangesPerRun {
		t.Fatalf("run range overflow changed bounded state: objects=%d budget=%+v",
			len(objects), runBudget)
	}
}

func distinctObjectRefs(start, count int) []fingerprint.ObjectReference {
	return distinctObjectRefsAtOrdinal(start, count, 1)
}

func distinctObjectRefsAtOrdinal(start, count int, ordinal int32) []fingerprint.ObjectReference {
	refs := make([]fingerprint.ObjectReference, 0, count)
	for i := 0; i < count; i++ {
		refs = append(refs, fingerprint.ObjectReference{
			Token:            "table_" + strconv.Itoa(start+i),
			Kind:             "table",
			Relation:         fingerprint.RelationTarget,
			StatementOrdinal: ordinal,
		})
	}
	return refs
}
