package technology

import (
	"path"
	"strings"
)

// Scala-specific detection signals and path conventions.
//
// They live in their own file so that adding the next language is an additive
// change: a new file with its signals, its entry in manifestSignals, and its
// toolchain. Nothing in the review pipeline has to know it happened.

// scalaLibrarySignals are the Scala dependencies worth reporting.
//
// Coordinates are matched as substrings of build.sbt, which is enough to be specific:
// "com.typesafe.play" does not appear in a build file by accident. The list is small
// on purpose — it exists to point the review at semantics that matter, not to
// inventory the ecosystem.
func scalaLibrarySignals() []librarySignal {
	return []librarySignal{
		{coordinate: "org.typelevel", label: "cats"},
		{coordinate: "cats-core", label: "cats"},
		{coordinate: "cats-effect", label: "cats-effect"},
		{coordinate: "com.typesafe.play", label: "play", framework: true},
		{coordinate: "org.playframework", label: "play", framework: true},
		{coordinate: "com.typesafe.akka", label: "akka", framework: true},
		{coordinate: "org.apache.pekko", label: "pekko", framework: true},
		{coordinate: "com.typesafe.slick", label: "slick"},
		{coordinate: "org.tpolecat", label: "doobie"},
		{coordinate: "doobie-core", label: "doobie"},
		{coordinate: "io.circe", label: "circe"},
		{coordinate: "dev.zio", label: "zio", framework: true},
		{coordinate: "org.scalatest", label: "scalatest"},
		{coordinate: "org.mongodb.scala", label: "mongo-scala"},
	}
}

// scalaTestSuffixes are the conventional endings of a Scala test file. Both the
// ScalaTest "Spec" convention and the JUnit-style "Test" convention are recognized,
// since a repository may use either and mislabelling a test as source would inflate
// its review priority.
var scalaTestSuffixes = []string{"spec", "test", "suite", "props", "properties"}

// IsScalaSource reports whether a path is a Scala source or build file.
func IsScalaSource(filePath string) bool {
	lower := strings.ToLower(strings.TrimSpace(filePath))
	return strings.HasSuffix(lower, ".scala") || strings.HasSuffix(lower, ".sbt")
}

// IsScalaTest reports whether a Scala path is a test.
//
// The judgement is by file name first — FooSpec.scala is a test wherever it lives —
// and by directory second, so a plain helper inside src/test/scala still counts.
func IsScalaTest(filePath string) bool {
	lower := strings.ToLower(strings.TrimSpace(filePath))
	if !strings.HasSuffix(lower, ".scala") {
		return false
	}

	stem := strings.TrimSuffix(path.Base(lower), ".scala")
	for _, suffix := range scalaTestSuffixes {
		if strings.HasSuffix(stem, suffix) && stem != suffix {
			return true
		}
	}

	return inScalaTestDirectory(lower)
}

// inScalaTestDirectory reports whether the path sits under a Scala test source root.
func inScalaTestDirectory(lowerPath string) bool {
	return strings.Contains(lowerPath, "src/test/") || strings.Contains(lowerPath, "src/it/")
}

// ScalaSourceForTest maps a Scala test path onto the source file it covers.
//
// The mapping is purely lexical: FooSpec.scala and FooTest.scala both point at
// Foo.scala, and the test's own directory is preserved only as a fallback because a
// Scala test conventionally lives under src/test while its subject lives under
// src/main. Both candidates are returned so a caller can match either. No symbol
// analysis is involved, and none is implied — this is a naming convention, not a
// claim about what the test actually exercises.
func ScalaSourceForTest(testPath string) ([]string, bool) {
	trimmed := strings.TrimSpace(testPath)
	if !strings.HasSuffix(strings.ToLower(trimmed), ".scala") {
		return nil, false
	}

	dir := path.Dir(trimmed)
	base := path.Base(trimmed)
	stem := strings.TrimSuffix(base, path.Ext(base))
	lowerStem := strings.ToLower(stem)

	subject := ""
	for _, suffix := range scalaTestSuffixes {
		if strings.HasSuffix(lowerStem, suffix) && lowerStem != suffix {
			subject = stem[:len(stem)-len(suffix)]
			break
		}
	}
	if subject == "" {
		return nil, false
	}

	sourceName := subject + ".scala"

	candidates := []string{path.Join(dir, sourceName)}
	if mainDir := strings.Replace(dir, "src/test/", "src/main/", 1); mainDir != dir {
		candidates = append(candidates, path.Join(mainDir, sourceName))
	}
	if mainDir := strings.Replace(dir, "src/it/", "src/main/", 1); mainDir != dir {
		candidates = append(candidates, path.Join(mainDir, sourceName))
	}

	return candidates, true
}

// IsSBTBuildFile reports whether a path is part of the sbt build definition.
//
// These files decide what the build does, so when Scala is under review they are
// worth more attention than an ordinary configuration file.
func IsSBTBuildFile(filePath string) bool {
	clean := strings.TrimPrefix(path.Clean(strings.TrimSpace(filePath)), "./")
	lower := strings.ToLower(clean)

	if strings.HasSuffix(lower, ".sbt") {
		return true
	}

	switch {
	case lower == "project/build.properties",
		strings.HasSuffix(lower, "/project/build.properties"):
		return true
	case strings.HasPrefix(lower, "project/") && strings.HasSuffix(lower, ".scala"):
		// project/*.scala is build code, not application code.
		return true
	}

	return false
}
