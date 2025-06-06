// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build !windows

package usm

import (
	"archive/zip"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/servicediscovery/envs"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/servicediscovery/language"
	"github.com/DataDog/datadog-agent/pkg/network/protocols/http/testutil"
)

const (
	springBootApp = "app/app.jar"

	// we need to use these non-descriptive shorter folder names because of the filename_linting
	// CI check that limits the number of characters in a path to 255.
	jbossTestAppRoot            = "../testdata/a"
	jbossTestAppRootAbsolute    = "/testdata/a"
	weblogicTestAppRoot         = "../testdata/b"
	weblogicTestAppRootAbsolute = "/testdata/b"
)

func MakeTestSubDirFS(t *testing.T) SubDirFS {
	curDir, err := testutil.CurDir()
	require.NoError(t, err)

	full := filepath.Join(curDir, "..", "..", "..", "..", "discovery", "testdata", "root")
	return NewSubDirFS(full)
}

func TestExtractServiceMetadata(t *testing.T) {
	springBootAppFullPath := createMockSpringBootApp(t)
	sub := MakeTestSubDirFS(t)
	usmFull, err := filepath.Abs("testdata/root")
	require.NoError(t, err)
	subUsmTestData := NewSubDirFS(usmFull)
	tests := []struct {
		name                        string
		cmdline                     []string
		envs                        map[string]string
		lang                        language.Language
		expectedGeneratedName       string
		expectedDDService           string
		expectedAdditionalServices  []string
		expectedGeneratedNameSource ServiceNameSource
		ddServiceInjected           bool
		fs                          *SubDirFS
		skipOnWindows               bool
	}{
		{
			name:                  "empty",
			cmdline:               []string{},
			expectedGeneratedName: "",
		},
		{
			name:                  "blank",
			cmdline:               []string{""},
			expectedGeneratedName: "",
		},
		{
			name: "single arg executable",
			cmdline: []string{
				"./my-server.sh",
			},
			expectedGeneratedName:       "my-server",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "single arg executable with DD_SERVICE",
			cmdline: []string{
				"./my-server.sh",
			},
			envs:                        map[string]string{"DD_SERVICE": "my-service"},
			expectedDDService:           "my-service",
			expectedGeneratedName:       "my-server",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "single arg executable with DD_TAGS",
			cmdline: []string{
				"./my-server.sh",
			},
			envs:                        map[string]string{"DD_TAGS": "service:my-service"},
			expectedDDService:           "my-service",
			expectedGeneratedName:       "my-server",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "single arg executable with special chars",
			cmdline: []string{
				"./-my-server.sh-",
			},
			expectedGeneratedName:       "my-server",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "sudo",
			cmdline: []string{
				"sudo", "-E", "-u", "dog", "/usr/local/bin/myApp", "-items=0,1,2,3", "-foo=bar",
			},
			expectedGeneratedName:       "myApp",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "python flask argument",
			cmdline: []string{
				"/opt/python/2.7.11/bin/python2.7", "flask", "run", "--host=0.0.0.0",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "flask",
			expectedGeneratedNameSource: Python,
			envs:                        map[string]string{"PWD": "testdata/python"},
			fs:                          &subUsmTestData,
		},
		{
			name: "python - flask argument in path",
			cmdline: []string{
				"/opt/python/2.7.11/bin/python2.7", "testdata/python/flask", "run", "--host=0.0.0.0", "--without-threads",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "flask",
			expectedGeneratedNameSource: Python,
			fs:                          &subUsmTestData,
		},
		{
			name: "python flask in single argument",
			cmdline: []string{
				"/opt/python/2.7.11/bin/python2.7 flask run --host=0.0.0.0",
			},
			lang:                        language.Python,
			envs:                        map[string]string{"PWD": "testdata/python"},
			expectedGeneratedName:       "flask",
			expectedGeneratedNameSource: Python,
			fs:                          &subUsmTestData,
		},
		{
			name: "python - module hello",
			cmdline: []string{
				"python3", "-m", "hello",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "hello",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "ruby - td-agent",
			cmdline: []string{
				"ruby", "/usr/sbin/td-agent", "--log", "/var/log/td-agent/td-agent.log", "--daemon", "/var/run/td-agent/td-agent.pid",
			},
			lang:                        language.Ruby,
			expectedGeneratedName:       "td-agent",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "ruby - puma with app name in brackets",
			cmdline: []string{
				"puma", "6.4.3", "(unix:///var/opt/app/sockets/app.socket,tcp://127.0.0.1:8080)", "[app-worker]",
			},
			lang:                        language.Ruby,
			expectedGeneratedName:       "app-worker",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "ruby - puma cluster worker with app name",
			cmdline: []string{
				"puma:", "cluster", "worker", "0:", "15381", "[app-worker]",
			},
			lang:                        language.Ruby,
			expectedGeneratedName:       "app-worker",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "ruby - puma with simple app name",
			cmdline: []string{
				"puma", "6.6.0", "(tcp://localhost:8080)", "[app]",
			},
			lang:                        language.Ruby,
			expectedGeneratedName:       "app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "ruby - puma without app name",
			cmdline: []string{
				"puma", "6.6.0", "(tcp://localhost:8080)",
			},
			lang:                        language.Ruby,
			expectedGeneratedName:       "puma",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java using the -jar flag to define the service",
			cmdline: []string{
				"java", "-Xmx4000m", "-Xms4000m", "-XX:ReservedCodeCacheSize=256m", "-jar", "/opt/sheepdog/bin/myservice.jar",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "myservice",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java using the -jar flag to point to a .war",
			cmdline: []string{
				"java", "-Duser.home=/var/jenkins_home", "-Dhudson.lifecycle=hudson.lifecycle.ExitLifecycle", "-jar", "/usr/share/jenkins/jenkins.war", "--httpPort=8000",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "jenkins",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java class name as service",
			cmdline: []string{
				"java", "-Xmx4000m", "-Xms4000m", "-XX:ReservedCodeCacheSize=256m", "com.datadog.example.HelloWorld",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "com.datadog.example.HelloWorld",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java -m flag",
			cmdline: []string{
				"java", "-Xmx4000m", "-Xms4000m", "-XX:ReservedCodeCacheSize=256m", "-m", "org.elasticsearch.server/org.elasticsearch.bootstrap.Elasticsearch",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "org.elasticsearch.bootstrap.Elasticsearch",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java --module flag",
			cmdline: []string{
				"java", "-Xmx4000m", "--module", "org.elasticsearch.server/org.elasticsearch.bootstrap.Elasticsearch", "-Xfoo",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "org.elasticsearch.bootstrap.Elasticsearch",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java -m flag after class",
			cmdline: []string{
				"java", "foo", "-m", "argument-to-app",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "foo",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java ignore @",
			cmdline: []string{
				"java", "@/tmp/foo21321312.tmp",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "java",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java ignore @2",
			cmdline: []string{
				"java", "@foo.extra", "myapp",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "myapp",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java kafka",
			cmdline: []string{
				"java", "-Xmx4000m", "-Xms4000m", "-XX:ReservedCodeCacheSize=256m", "kafka.Kafka",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "kafka.Kafka",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java parsing for org.apache projects with cassandra as the service",
			cmdline: []string{
				"/usr/bin/java", "-Xloggc:/usr/share/cassandra/logs/gc.log", "-ea", "-XX:+HeapDumpOnOutOfMemoryError", "-Xss256k", "-Dlogback.configurationFile=logback.xml",
				"-Dcassandra.logdir=/var/log/cassandra", "-Dcassandra.storagedir=/data/cassandra",
				"-cp", "/etc/cassandra:/usr/share/cassandra/lib/HdrHistogram-2.1.9.jar:/usr/share/cassandra/lib/cassandra-driver-core-3.0.1-shaded.jar",
				"org.apache.cassandra.service.CassandraDaemon",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "cassandra",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "java space in java executable path",
			cmdline: []string{
				"/home/dd/my java dir/java", "com.dog.cat",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "com.dog.cat",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "node js with package.json not present",
			cmdline: []string{
				"/usr/bin/node",
				"--require",
				"/private/node-patches_legacy/register.js",
				"--preserve-symlinks-main",
				"--",
				"/somewhere/index.js",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "node",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "node js with a broken package.json",
			cmdline: []string{
				"/usr/bin/node",
				"./testdata/inner/app.js",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "app",
			expectedGeneratedNameSource: CommandLine,
			fs:                          &subUsmTestData,
		},
		{
			name: "node js with a broken package.json",
			cmdline: []string{
				"/usr/bin/node",
				"./testdata/inner/link",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "link",
			expectedGeneratedNameSource: CommandLine,
			fs:                          &subUsmTestData,
		},
		{
			name: "node js with a valid package.json",
			cmdline: []string{
				"/usr/bin/node",
				"--require",
				"/private/node-patches_legacy/register.js",
				"--preserve-symlinks-main",
				"--",
				"./testdata/index.js",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "my-awesome-package",
			fs:                          &subUsmTestData,
			expectedGeneratedNameSource: Nodejs,
		},
		{
			name: "nodejs .cjs with a valid package.json",
			cmdline: []string{
				"/usr/bin/node",
				"./testdata/foo.cjs",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "my-awesome-package",
			expectedGeneratedNameSource: Nodejs,
			fs:                          &subUsmTestData,
		},
		{
			name: "nodejs .mjs with a valid package.json",
			cmdline: []string{
				"/usr/bin/node",
				"./testdata/bar.mjs",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "my-awesome-package",
			expectedGeneratedNameSource: Nodejs,
			fs:                          &subUsmTestData,
		},
		{
			name: "node js with a symlink to a .js file and valid package.json",
			cmdline: []string{
				"/usr/bin/node",
				"--foo",
				"./testdata/bins/notjs",
				"--bar",
				"./testdata/bins/broken",
				"./testdata/bins/json-server",
			},
			lang:                        language.Node,
			expectedGeneratedName:       "json-server-package",
			expectedGeneratedNameSource: Nodejs,
			skipOnWindows:               true,
			fs:                          &subUsmTestData,
		},
		{
			name: "node js with a valid nested package.json and cwd",
			cmdline: []string{
				"/usr/bin/node",
				"--require",
				"/private/node-patches_legacy/register.js",
				"--preserve-symlinks-main",
				"--",
				"index.js",
			},
			lang:                        language.Node,
			envs:                        map[string]string{"PWD": "testdata/deep"}, // it's relative but it's ok for testing purposes
			fs:                          &subUsmTestData,
			expectedGeneratedName:       "my-awesome-package",
			expectedGeneratedNameSource: Nodejs,
		},
		{
			name: "spring boot default options",
			cmdline: []string{
				"java",
				"-jar",
				springBootAppFullPath,
			},
			lang:                        language.Java,
			expectedGeneratedName:       "default-app",
			expectedGeneratedNameSource: Spring,
		},
		{
			name: "spring boot unpacked jar with new launcher",
			cmdline: []string{
				"java",
				"-jar",
				springBootLauncher,
			},
			lang:                        language.Java,
			expectedGeneratedName:       "spring-boot-app-name",
			expectedGeneratedNameSource: Spring,
			envs:                        map[string]string{"PWD": "testdata/spring/with-prop"},
			fs:                          &sub,
		},
		{
			name: "spring boot unpacked jar with classpath",
			cmdline: []string{
				"java",
				"-cp",
				"spring/with-prop:foo",
				"-jar",
				springBootLauncher,
			},
			lang:                        language.Java,
			expectedGeneratedName:       "spring-boot-app-name",
			expectedGeneratedNameSource: Spring,
			envs:                        map[string]string{"PWD": "testdata"},
			fs:                          &sub,
		},
		{
			name: "spring boot unpacked jar with no properties",
			cmdline: []string{
				"java",
				"-jar",
				springBootLauncher,
			},
			lang:                        language.Java,
			expectedGeneratedName:       "com.example.spring_boot.ApplicationKtx",
			expectedGeneratedNameSource: Spring,
			envs:                        map[string]string{"PWD": "testdata/spring/without-prop"},
			fs:                          &sub,
		},
		{
			name: "spring boot unpacked jar with old launcher",
			cmdline: []string{
				"java",
				"-jar",
				springBootOldLauncher,
			},
			lang:                        language.Java,
			expectedGeneratedName:       "spring-boot-app-name",
			expectedGeneratedNameSource: Spring,
			envs:                        map[string]string{"PWD": "testdata/spring/with-prop"},
			fs:                          &sub,
		},
		{
			name: "wildfly 18 standalone",
			cmdline: []string{
				"home/app/.sdkman/candidates/java/17.0.4.1-tem/bin/java",
				"-D[Standalone]",
				"-server",
				"-Xms64m",
				"-Xmx512m",
				"-XX:MetaspaceSize=96M",
				"-XX:MaxMetaspaceSize=256m",
				"-Djava.net.preferIPv4Stack=true",
				"-Djboss.modules.system.pkgs=org.jboss.byteman",
				"-Djava.awt.headless=true",
				"--add-exports=java.base/sun.nio.ch=ALL-UNNAMED",
				"--add-exports=jdk.unsupported/sun.misc=ALL-UNNAMED",
				"--add-exports=jdk.unsupported/sun.reflect=ALL-UNNAMED",
				"-Dorg.jboss.boot.log.file=" + jbossTestAppRoot + "/standalone/log/server.log",
				"-Dlogging.configuration=file:" + jbossTestAppRoot + "/standalone/configuration/logging.properties",
				"-jar",
				"" + jbossTestAppRoot + "/jboss-modules.jar",
				"-mp",
				"" + jbossTestAppRoot + "/modules",
				"org.jboss.as.standalone",
				"-Djboss.home.dir=" + jbossTestAppRoot,
				"-Djboss.server.base.dir=" + jbossTestAppRoot + "/standalone",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "jboss-modules",
			expectedAdditionalServices:  []string{"my-jboss-webapp", "some_context_root", "web3"},
			fs:                          &sub,
			envs:                        map[string]string{"PWD": "/sibiling"},
			expectedGeneratedNameSource: JBoss,
		},
		{
			name: "wildfly 18 domain",
			cmdline: []string{
				"/home/app/.sdkman/candidates/java/17.0.4.1-tem/bin/java",
				"--add-exports=java.base/sun.nio.ch=ALL-UNNAMED",
				"--add-exports=jdk.unsupported/sun.reflect=ALL-UNNAMED",
				"--add-exports=jdk.unsupported/sun.misc=ALL-UNNAMED",
				"-D[Server:server-one]",
				"-D[pcid:780891833]",
				"-Xms64m",
				"-Xmx512m",
				"-server",
				"-XX:MetaspaceSize=96m",
				"-XX:MaxMetaspaceSize=256m",
				"-Djava.awt.headless=true",
				"-Djava.net.preferIPv4Stack=true",
				"-Djboss.home.dir=" + jbossTestAppRoot,
				"-Djboss.modules.system.pkgs=org.jboss.byteman",
				"-Djboss.server.log.dir=" + jbossTestAppRoot + "/domain/servers/server-one/log",
				"-Djboss.server.temp.dir=" + jbossTestAppRoot + "/domain/servers/server-one/tmp",
				"-Djboss.server.data.dir=" + jbossTestAppRoot + "/domain/servers/server-one/data",
				"-Dorg.jboss.boot.log.file=" + jbossTestAppRoot + "/domain/servers/server-one/log/server.log",
				"-Dlogging.configuration=file:" + jbossTestAppRoot + "/domain/configuration/default-server-logging.properties",
				"-jar",
				"" + jbossTestAppRoot + "/jboss-modules.jar",
				"-mp",
				"" + jbossTestAppRoot + "/modules",
				"org.jboss.as.server",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "jboss-modules",
			expectedGeneratedNameSource: JBoss,
			expectedAdditionalServices:  []string{"web3", "web4"},
			fs:                          &sub,
			envs:                        map[string]string{"PWD": "/sibiling"},
		},
		{
			name: "weblogic 12",
			fs:   &sub,
			cmdline: []string{
				"/u01/jdk/bin/java",
				"-Djava.security.egd=file:/dev/./urandom",
				"-cp",
				"/u01/oracle/wlserver/server/lib/weblogic-launcher.jar",
				"-Dlaunch.use.env.classpath=true",
				"-Dweblogic.Name=AdminServer",
				"-Djava.security.policy=/u01/oracle/wlserver/server/lib/weblogic.policy",
				"-Djava.system.class.loader=com.oracle.classloader.weblogic.LaunchClassLoader",
				"-javaagent:/u01/oracle/wlserver/server/lib/debugpatch-agent.jar",
				"-da",
				"-Dwls.home=/u01/oracle/wlserver/server",
				"-Dweblogic.home=/u01/oracle/wlserver/server",
				"weblogic.Server",
			},
			lang:                        language.Java,
			envs:                        map[string]string{"PWD": weblogicTestAppRootAbsolute},
			expectedGeneratedName:       "weblogic.Server",
			expectedGeneratedNameSource: WebLogic,
			expectedAdditionalServices:  []string{"my_context", "sample4", "some_context_root"},
		},
		{
			name: "java with dd_service as system property",
			cmdline: []string{
				"/usr/bin/java", "-Ddd.service=custom", "-jar", "app.jar",
			},
			lang:                        language.Java,
			expectedDDService:           "custom",
			expectedGeneratedName:       "app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			// The system property takes priority over the environment variable, see
			// https://docs.datadoghq.com/tracing/trace_collection/library_config/java/
			name: "java with dd_service as system property and DD_SERVICE",
			cmdline: []string{
				"/usr/bin/java", "-Ddd.service=dd-service-from-property", "-jar", "app.jar",
			},
			lang:                        language.Java,
			envs:                        map[string]string{"DD_SERVICE": "dd-service-from-env"},
			expectedDDService:           "dd-service-from-property",
			expectedGeneratedName:       "app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "Tomcat 10.X",
			cmdline: []string{
				"/usr/bin/java",
				"-Djava.util.logging.config.file=testdata/tomcat/conf/logging.properties",
				"-Djava.util.logging.manager=org.apache.juli.ClassLoaderLogManager",
				"-Djdk.tls.ephemeralDHKeySize=2048",
				"-Djava.protocol.handler.pkgs=org.apache.catalina.webresources",
				"-Dorg.apache.catalina.security.SecurityListener.UMASK=0027",
				"--add-opens=java.base/java.lang=ALL-UNNAMED",
				"--add-opens=java.base/java.io=ALL-UNNAMED",
				"--add-opens=java.base/java.util=ALL-UNNAMED",
				"--add-opens=java.base/java.util.concurrent=ALL-UNNAMED",
				"--add-opens=java.rmi/sun.rmi.transport=ALL-UNNAMED",
				"-classpath",
				"testdata/tomcat/bin/bootstrap.jar:testdata/tomcat/bin/tomcat-juli.jar",
				"-Dcatalina.base=testdata/tomcat",
				"-Dcatalina.home=testdata/tomcat",
				"-Djava.io.tmpdir=testdata/tomcat/temp",
				"org.apache.catalina.startup.Bootstrap",
				"start",
			},
			lang:                        language.Java,
			expectedGeneratedName:       "catalina",
			expectedGeneratedNameSource: Tomcat,
			expectedAdditionalServices:  []string{"app2", "custom"},
			fs:                          &subUsmTestData,
		},
		{
			name: "dotnet cmd with dll",
			cmdline: []string{
				"/usr/bin/dotnet", "./myservice.dll",
			},
			lang:                        language.DotNet,
			expectedGeneratedName:       "myservice",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "dotnet cmd with dll and options",
			cmdline: []string{
				"/usr/bin/dotnet", "-v", "--", "/app/lib/myservice.dll",
			},
			lang:                        language.DotNet,
			expectedGeneratedName:       "myservice",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "dotnet cmd with unrecognized options",
			cmdline: []string{
				"/usr/bin/dotnet", "run", "--project", "./projects/proj1/proj1.csproj",
			},
			lang:                        language.DotNet,
			expectedGeneratedName:       "dotnet",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "PHP Laravel",
			cmdline: []string{
				"php",
				"artisan",
				"serve",
			},
			lang:                        language.PHP,
			expectedGeneratedName:       "laravel",
			expectedGeneratedNameSource: Laravel,
		},
		{
			name: "Plain PHP with INI",
			cmdline: []string{
				"php",
				"-ddatadog.service=foo",
				"swoole-server.php",
			},
			lang:                        language.PHP,
			expectedDDService:           "foo",
			expectedGeneratedName:       "php",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "PHP with version number",
			cmdline: []string{
				"php8.3",
				"artisan",
				"migrate:fresh",
			},
			lang:                        language.PHP,
			expectedGeneratedName:       "laravel",
			expectedGeneratedNameSource: Laravel,
		},
		{
			name: "PHP with two-digit version number",
			cmdline: []string{
				"php8.10",
				"artisan",
				"migrate:fresh",
			},
			lang:                        language.PHP,
			expectedGeneratedName:       "laravel",
			expectedGeneratedNameSource: Laravel,
		},
		{
			name: "PHP-FPM shouldn't trigger php parsing",
			cmdline: []string{
				"php-fpm",
				"artisan",
			},
			expectedGeneratedName:       "php-fpm",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "PHP-FPM with version number shouldn't trigger php parsing",
			cmdline: []string{
				"php8.1-fpm",
				"artisan",
			},
			expectedGeneratedName:       "php8",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name:                        "DD_SERVICE_set_manually",
			cmdline:                     []string{"java", "-jar", "Foo.jar"},
			lang:                        language.Java,
			envs:                        map[string]string{"DD_SERVICE": "howdy"},
			expectedDDService:           "howdy",
			expectedGeneratedName:       "Foo",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name:                        "DD_SERVICE_set_manually_tags",
			cmdline:                     []string{"java", "-jar", "Foo.jar"},
			lang:                        language.Java,
			envs:                        map[string]string{"DD_TAGS": "service:howdy"},
			expectedDDService:           "howdy",
			expectedGeneratedName:       "Foo",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name:                        "DD_SERVICE_set_manually_injection",
			cmdline:                     []string{"java", "-jar", "Foo.jar"},
			lang:                        language.Java,
			envs:                        map[string]string{"DD_SERVICE": "howdy", "DD_INJECTION_ENABLED": "tracer,service_name"},
			expectedDDService:           "howdy",
			expectedGeneratedName:       "Foo",
			expectedGeneratedNameSource: CommandLine,
			ddServiceInjected:           true,
		},
		{
			name: "gunicorn simple",
			cmdline: []string{
				"gunicorn",
				"--workers=2",
				"test:app",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "test",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn simple with python",
			cmdline: []string{
				"/usr/bin/python3",
				"/usr/bin/gunicorn",
				"--workers=2",
				"foo:create_app()",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "foo",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn from name",
			cmdline: []string{
				"gunicorn",
				"--workers=2",
				"-b",
				"0.0.0.0",
				"-n",
				"dummy",
				"test:app",
			},
			expectedGeneratedName:       "dummy",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn from name (long arg)",
			cmdline: []string{
				"gunicorn",
				"--workers=2",
				"-b",
				"0.0.0.0",
				"--name=dummy",
				"test:app",
			},
			expectedGeneratedName:       "dummy",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn from name in env",
			cmdline: []string{
				"gunicorn",
				"test:app",
			},
			envs:                        map[string]string{"GUNICORN_CMD_ARGS": "--bind=127.0.0.1:8080 --workers=3 -n dummy"},
			expectedGeneratedName:       "dummy",
			expectedGeneratedNameSource: Gunicorn,
		},
		{
			name: "gunicorn without app found",
			cmdline: []string{
				"gunicorn",
			},
			envs:                        map[string]string{"GUNICORN_CMD_ARGS": "--bind=127.0.0.1:8080 --workers=3"},
			expectedGeneratedName:       "gunicorn",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn with partial wsgi app",
			cmdline: []string{
				"gunicorn",
				"my.package",
			},
			expectedGeneratedName:       "my.package",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn with empty WSGI_APP env",
			cmdline: []string{
				"gunicorn",
				"my.package",
			},
			envs:                        map[string]string{"WSGI_APP": ""},
			expectedGeneratedName:       "my.package",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn with WSGI_APP env",
			cmdline: []string{
				"gunicorn",
			},
			envs:                        map[string]string{"WSGI_APP": "test:app"},
			expectedGeneratedName:       "test",
			expectedGeneratedNameSource: Gunicorn,
		},
		{
			name: "gunicorn with replaced cmdline with colon",
			cmdline: []string{
				"gunicorn:",
				"master",
				"[domains.foo.apps.bar:create_server()]",
			},
			expectedGeneratedName:       "domains.foo.apps.bar",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn with replaced cmdline",
			cmdline: []string{
				"gunicorn:",
				"master",
				"[mcservice]",
			},
			expectedGeneratedName:       "mcservice",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "gunicorn with replaced cmdline and [ready]",
			cmdline: []string{
				"[ready]",
				"gunicorn:",
				"worker",
				"[airflow-webserver]",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "airflow-webserver",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "uvicorn with first arg",
			cmdline: []string{
				"/usr/local/bin/python",
				"/usr/local/bin/uvicorn",
				"myapp.asgi:application",
				"--host=0.0.0.0",
				"--port=8000",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "myapp.asgi",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "uvicorn with middle args",
			cmdline: []string{
				"/app/.venv/bin/python3",
				"/app/.venv/bin/uvicorn",
				"--factory",
				"--host=0.0.0.0",
				"--port=8000",
				"app:create_app",
				"--workers=4",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "uvicorn with header",
			cmdline: []string{
				"/usr/local/bin/python3",
				"/usr/local/bin/uvicorn",
				"--header=X-Foo:Bar",
				"api.v1.app:app",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "api.v1.app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "uvicorn with header separate",
			cmdline: []string{
				"/usr/local/bin/python3",
				"/usr/local/bin/uvicorn",
				"--header",
				"X-Foo:Bar",
				"api.v1.app:app",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "api.v1.app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "uvicorn with header separate last",
			cmdline: []string{
				"/usr/local/bin/python3",
				"/usr/local/bin/uvicorn",
				"api.v1.app:app",
				"--header",
				"X-Foo:Bar",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "api.v1.app",
			expectedGeneratedNameSource: CommandLine,
		},
		{
			name: "uvicorn unknown",
			cmdline: []string{
				"/usr/local/bin/python3",
				"/usr/local/bin/uvicorn",
				"foo",
			},
			lang:                        language.Python,
			expectedGeneratedName:       "uvicorn",
			expectedGeneratedNameSource: CommandLine,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fs fs.SubFS
			fs = RealFs{}
			if tt.fs != nil {
				fs = *tt.fs
			}
			ctx := NewDetectionContext(tt.cmdline, envs.NewVariables(tt.envs), fs)
			ctx.ContextMap = make(DetectorContextMap)
			meta, ok := ExtractServiceMetadata(tt.lang, ctx)
			if len(tt.expectedGeneratedName) == 0 && len(tt.expectedDDService) == 0 {
				require.False(t, ok)
			} else {
				require.True(t, ok)
				require.Equal(t, tt.expectedDDService, meta.DDService)
				require.Equal(t, tt.expectedGeneratedName, meta.Name)
				require.Equal(t, tt.expectedAdditionalServices, meta.AdditionalNames)
				require.Equal(t, tt.ddServiceInjected, meta.DDServiceInjected)
				require.Equal(t, tt.expectedGeneratedNameSource, meta.Source)
			}
		})
	}
}

func writeFile(writer *zip.Writer, name string, content string) error {
	w, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(content))
	return err
}

type chainedFS struct {
	chain []fs.FS
}

func (c chainedFS) Open(name string) (fs.File, error) {
	var err error
	for _, current := range c.chain {
		var f fs.File
		f, err = current.Open(name)
		if err == nil {
			return f, nil
		}
	}
	return nil, err
}

func (c chainedFS) Sub(dir string) (fs.FS, error) {
	for _, current := range c.chain {
		if sub, ok := current.(fs.SubFS); ok {
			return sub.Sub(dir)
		}
	}
	return nil, errors.New("no suitable SubFS in the chain")
}

type shadowFS struct {
	filesystem fs.FS
	parent     fs.FS
	globs      []string
}

func (s shadowFS) Open(name string) (fs.File, error) {
	var fsys fs.FS
	if s.parent != nil {
		fsys = s.parent
	} else {
		fsys = s.filesystem
	}
	for _, current := range s.globs {
		ok, err := path.Match(current, name)
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, fs.ErrNotExist
		}
	}
	return fsys.Open(name)
}

func (s shadowFS) Sub(dir string) (fs.FS, error) {
	fsys, err := fs.Sub(s.filesystem, dir)
	if err != nil {
		return nil, err
	}
	return shadowFS{filesystem: fsys, parent: s}, nil
}

func TestSubDirFS(t *testing.T) {
	fs := NewSubDirFS("testdata/root/")
	_, err := fs.Stat("/testdata/index.js")
	require.NoError(t, err)

	_, err = fs.Stat("testdata/index.js")
	require.NoError(t, err)

	_, err = fs.Stat("../root")
	require.Error(t, err)

	_, err = fs.Stat("/testdata/python/../index.js")
	require.NoError(t, err)

	_, err = fs.Stat("testdata/python/../index.js")
	require.NoError(t, err)

	f, err := fs.Open("testdata/python/../index.js")
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })

	sub, err := fs.Sub("testdata")
	require.NoError(t, err)
	f2, err := sub.Open("index.js")
	require.NoError(t, err)
	t.Cleanup(func() { f2.Close() })

	entries, err := fs.ReadDir("/testdata")
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	require.Contains(t, names, "index.js")
}
