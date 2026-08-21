package technology

import (
	"strings"
	"testing"
)

func TestScalaVersionLabels(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "scala 3",
			content: `ThisBuild / scalaVersion := "3.3.1"`,
			want:    []string{"scala-3"},
		},
		{
			name:    "scala 2.13",
			content: `scalaVersion := "2.13.12"`,
			want:    []string{"scala-2"},
		},
		{
			name:    "cross-built declares both",
			content: `crossScalaVersions := Seq("2.13.12", "3.3.1")`,
			want:    []string{"scala-2", "scala-3"},
		},
		{
			name:    "version in a val",
			content: "val scala3 = \"3.4.0\"\nscalaVersion := scala3",
			want:    nil, // an indirection this deliberately does not follow
		},
		{
			name:    "no declaration",
			content: `libraryDependencies += "dev.zio" %% "zio" % "2.1.1"`,
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scalaVersionLabels(tc.content)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("labels = %v, want %v", got, tc.want)
			}
		})
	}
}

// A build file is full of library versions. Reading those would report whatever the
// newest dependency happens to be as the language version.
func TestLibraryVersionsAreNotScalaVersions(t *testing.T) {
	content := `
libraryDependencies ++= Seq(
  "dev.zio"       %% "zio"        % "2.1.1",
  "org.typelevel" %% "cats-core"  % "2.10.0",
  "io.circe"      %% "circe-core" % "3.0.0"
)
`
	if got := scalaVersionLabels(content); len(got) != 0 {
		t.Errorf("labels = %v, want none; only a scalaVersion declaration counts", got)
	}
}

// The version travels with the rest of the profile, so review guidance can key off it.
func TestScalaVersionReachesTheProfile(t *testing.T) {
	profile := DetectFromSignals(map[string]string{
		"build.sbt": `
ThisBuild / scalaVersion := "3.3.1"
libraryDependencies ++= Seq(
  "dev.zio"       %% "zio"       % "2.1.1",
  "org.typelevel" %% "cats-core" % "2.10.0"
)
`,
	}, []string{"src/main/scala/App.scala"})

	for _, want := range []string{"scala-3", "cats"} {
		if !profile.HasTechnology(want) {
			t.Errorf("profile technologies = %v, want %s", profile.Technologies(), want)
		}
	}
	if !profile.HasTechnology("zio") {
		t.Errorf("profile technologies = %v, want zio", profile.Technologies())
	}
	if profile.HasTechnology("scala-2") {
		t.Errorf("profile claims scala-2 for a Scala 3 build: %v", profile.Technologies())
	}
}
