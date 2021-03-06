package model

import (
	"testing"

	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/testutil"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/assert"
)

func TestFindOneProjectVar(t *testing.T) {
	Convey("With an existing repository var", t, func() {
		testutil.HandleTestingErr(db.Clear(ProjectVarsCollection), t,
			"Error clearing collection")
		vars := map[string]string{
			"a": "b",
			"c": "d",
		}
		projectVars := ProjectVars{
			Id:   "mongodb",
			Vars: vars,
		}

		Convey("all fields should be returned accurately for the "+
			"corresponding project vars", func() {
			_, err := projectVars.Upsert()
			So(err, ShouldBeNil)
			projectVarsFromDB, err := FindOneProjectVars("mongodb")
			So(err, ShouldBeNil)
			So(projectVarsFromDB.Id, ShouldEqual, "mongodb")
			So(projectVarsFromDB.Vars, ShouldResemble, vars)
		})
	})
}

func TestProjectVarsInsert(t *testing.T) {
	assert := assert.New(t)

	testutil.HandleTestingErr(db.Clear(ProjectVarsCollection), t,
		"Error clearing collection")

	vars := &ProjectVars{Id: "mongodb", Vars: map[string]string{"a": "1"}}
	assert.NoError(vars.Insert())

	projectVarsFromDB, err := FindOneProjectVars("mongodb")
	assert.NoError(err)
	assert.Equal("mongodb", projectVarsFromDB.Id)
	assert.NotEmpty(projectVarsFromDB.Vars)
	assert.Equal("1", projectVarsFromDB.Vars["a"])
}

func TestRedactPrivateVars(t *testing.T) {
	Convey("With vars", t, func() {
		vars := map[string]string{
			"a": "a",
			"b": "b",
		}
		privateVars := map[string]bool{
			"a": true,
		}
		projectVars := ProjectVars{
			Id:          "mongodb",
			Vars:        vars,
			PrivateVars: privateVars,
		}

		Convey("then redacting should return empty strings for private vars", func() {
			projectVars.RedactPrivateVars()
			So(projectVars.Vars["a"], ShouldEqual, "")
		})
	})
}
