// Copyright (c) 2016-2019 Tigera, Inc. All rights reserved.

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

package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/projectcalico/libcalico-go/lib/apiconfig"
	"github.com/projectcalico/libcalico-go/lib/names"
	"github.com/projectcalico/libcalico-go/lib/numorstring"
)

var (
	// RegexpIfaceElemRegexp matches an individual element in the overall interface list;
	// assumes the value represents a regular expression and is marked by '/' at the start
	// and end and cannot have spaces
	RegexpIfaceElemRegexp = regexp.MustCompile(`^\/[^\s]+\/$`)
	// NonRegexpIfaceElemRegexp matches an individual element in the overall interface list;
	// assumes the value is between 1-15 chars long and only be alphanumeric or - or _
	NonRegexpIfaceElemRegexp = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}$`)
	IfaceListRegexp          = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,15}(,[a-zA-Z0-9_-]{1,15})*$`)
	AuthorityRegexp          = regexp.MustCompile(`^[^:/]+:\d+$`)
	HostnameRegexp           = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	StringRegexp             = regexp.MustCompile(`^.*$`)
	IfaceParamRegexp         = regexp.MustCompile(`^[a-zA-Z0-9:._+-]{1,15}$`)
	// Hostname  have to be valid ipv4, ipv6 or strings up to 64 characters.
	HostAddressRegexp = regexp.MustCompile(`^[a-zA-Z0-9:._+-]{1,64}$`)
)

const (
	maxUint = ^uint(0)
	maxInt  = int(maxUint >> 1)
	minInt  = -maxInt - 1
)

// Source of a config value.  Values from higher-numbered sources override
// those from lower-numbered sources.  Note: some parameters (such as those
// needed to connect to the datastore) can only be set from a local source.
type Source uint8

const (
	Default = iota
	DatastoreGlobal
	DatastorePerHost
	ConfigFile
	EnvironmentVariable
)

var SourcesInDescendingOrder = []Source{EnvironmentVariable, ConfigFile, DatastorePerHost, DatastoreGlobal}

func (source Source) String() string {
	switch source {
	case Default:
		return "<default>"
	case DatastoreGlobal:
		return "datastore (global)"
	case DatastorePerHost:
		return "datastore (per-host)"
	case ConfigFile:
		return "config file"
	case EnvironmentVariable:
		return "environment variable"
	}
	return fmt.Sprintf("<unknown(%v)>", uint8(source))
}

func (source Source) Local() bool {
	switch source {
	case Default, ConfigFile, EnvironmentVariable:
		return true
	default:
		return false
	}
}

// Config contains the best, parsed config values loaded from the various sources.
// We use tags to control the parsing and validation.
type Config struct {
	// Configuration parameters.
	UseInternalDataplaneDriver bool   `config:"bool;true"`
	DataplaneDriver            string `config:"file(must-exist,executable);calico-iptables-plugin;non-zero,die-on-fail,skip-default-validation"`

	DatastoreType string `config:"oneof(kubernetes,etcdv3);etcdv3;non-zero,die-on-fail,local"`

	FelixHostname string `config:"hostname;;local,non-zero"`

	EtcdAddr      string   `config:"authority;127.0.0.1:2379;local"`
	EtcdScheme    string   `config:"oneof(http,https);http;local"`
	EtcdKeyFile   string   `config:"file(must-exist);;local"`
	EtcdCertFile  string   `config:"file(must-exist);;local"`
	EtcdCaFile    string   `config:"file(must-exist);;local"`
	EtcdEndpoints []string `config:"endpoint-list;;local"`

	TyphaAddr           string        `config:"authority;;local"`
	TyphaK8sServiceName string        `config:"string;;local"`
	TyphaK8sNamespace   string        `config:"string;kube-system;non-zero,local"`
	TyphaReadTimeout    time.Duration `config:"seconds;30;local"`
	TyphaWriteTimeout   time.Duration `config:"seconds;10;local"`

	// Client-side TLS config for Felix's communication with Typha.  If any of these are
	// specified, they _all_ must be - except that either TyphaCN or TyphaURISAN may be left
	// unset.  Felix will then initiate a secure (TLS) connection to Typha.  Typha must present
	// a certificate signed by a CA in TyphaCAFile, and with CN matching TyphaCN or URI SAN
	// matching TyphaURISAN.
	TyphaKeyFile  string `config:"file(must-exist);;local"`
	TyphaCertFile string `config:"file(must-exist);;local"`
	TyphaCAFile   string `config:"file(must-exist);;local"`
	TyphaCN       string `config:"string;;local"`
	TyphaURISAN   string `config:"string;;local"`

	Ipv6Support    bool `config:"bool;true"`
	IgnoreLooseRPF bool `config:"bool;false"`

	IptablesBackend                    string        `config:"oneof(legacy,nft);legacy"`
	RouteRefreshInterval               time.Duration `config:"seconds;90"`
	IptablesRefreshInterval            time.Duration `config:"seconds;90"`
	IptablesPostWriteCheckIntervalSecs time.Duration `config:"seconds;1"`
	IptablesLockFilePath               string        `config:"file;/run/xtables.lock"`
	IptablesLockTimeoutSecs            time.Duration `config:"seconds;0"`
	IptablesLockProbeIntervalMillis    time.Duration `config:"millis;50"`
	IpsetsRefreshInterval              time.Duration `config:"seconds;10"`
	MaxIpsetSize                       int           `config:"int;1048576;non-zero"`
	XDPRefreshInterval                 time.Duration `config:"seconds;90"`

	PolicySyncPathPrefix string `config:"file;;"`

	NetlinkTimeoutSecs time.Duration `config:"seconds;10"`

	MetadataAddr string `config:"hostname;127.0.0.1;die-on-fail"`
	MetadataPort int    `config:"int(0,65535);8775;die-on-fail"`

	OpenstackRegion string `config:"region;;die-on-fail"`

	InterfacePrefix  string           `config:"iface-list;cali;non-zero,die-on-fail"`
	InterfaceExclude []*regexp.Regexp `config:"iface-list-regexp;kube-ipvs0"`

	ChainInsertMode             string `config:"oneof(insert,append);insert;non-zero,die-on-fail"`
	DefaultEndpointToHostAction string `config:"oneof(DROP,RETURN,ACCEPT);DROP;non-zero,die-on-fail"`
	IptablesFilterAllowAction   string `config:"oneof(ACCEPT,RETURN);ACCEPT;non-zero,die-on-fail"`
	IptablesMangleAllowAction   string `config:"oneof(ACCEPT,RETURN);ACCEPT;non-zero,die-on-fail"`
	LogPrefix                   string `config:"string;calico-packet"`

	LogFilePath string `config:"file;/var/log/calico/felix.log;die-on-fail"`

	LogSeverityFile   string `config:"oneof(DEBUG,INFO,WARNING,ERROR,FATAL);INFO"`
	LogSeverityScreen string `config:"oneof(DEBUG,INFO,WARNING,ERROR,FATAL);INFO"`
	LogSeveritySys    string `config:"oneof(DEBUG,INFO,WARNING,ERROR,FATAL);INFO"`

	VXLANEnabled        bool   `config:"bool;false"`
	VXLANPort           int    `config:"int;4789"`
	VXLANVNI            int    `config:"int;4096"`
	VXLANMTU            int    `config:"int;1410;non-zero"`
	IPv4VXLANTunnelAddr net.IP `config:"ipv4;"`
	VXLANTunnelMACAddr  string `config:"string;"`

	IpInIpEnabled    bool   `config:"bool;false"`
	IpInIpMtu        int    `config:"int;1440;non-zero"`
	IpInIpTunnelAddr net.IP `config:"ipv4;"`

	ReportingIntervalSecs time.Duration `config:"seconds;30"`
	ReportingTTLSecs      time.Duration `config:"seconds;90"`

	EndpointReportingEnabled   bool          `config:"bool;false"`
	EndpointReportingDelaySecs time.Duration `config:"seconds;1"`

	IptablesMarkMask uint32 `config:"mark-bitmask;0xffff0000;non-zero,die-on-fail"`

	DisableConntrackInvalidCheck bool `config:"bool;false"`

	HealthEnabled                   bool   `config:"bool;false"`
	HealthPort                      int    `config:"int(0,65535);9099"`
	HealthHost                      string `config:"host-address;localhost"`
	PrometheusMetricsEnabled        bool   `config:"bool;false"`
	PrometheusMetricsHost           string `config:"host-address;"`
	PrometheusMetricsPort           int    `config:"int(0,65535);9091"`
	PrometheusGoMetricsEnabled      bool   `config:"bool;true"`
	PrometheusProcessMetricsEnabled bool   `config:"bool;true"`

	FailsafeInboundHostPorts  []ProtoPort `config:"port-list;tcp:22,udp:68,tcp:179,tcp:2379,tcp:2380,tcp:6666,tcp:6667;die-on-fail"`
	FailsafeOutboundHostPorts []ProtoPort `config:"port-list;udp:53,udp:67,tcp:179,tcp:2379,tcp:2380,tcp:6666,tcp:6667;die-on-fail"`

	KubeNodePortRanges []numorstring.Port `config:"portrange-list;30000:32767"`
	NATPortRange       numorstring.Port   `config:"portrange;"`
	NATOutgoingAddress net.IP             `config:"ipv4;"`

	UsageReportingEnabled          bool          `config:"bool;true"`
	UsageReportingInitialDelaySecs time.Duration `config:"seconds;300"`
	UsageReportingIntervalSecs     time.Duration `config:"seconds;86400"`
	ClusterGUID                    string        `config:"string;baddecaf"`
	ClusterType                    string        `config:"string;"`
	CalicoVersion                  string        `config:"string;"`

	ExternalNodesCIDRList []string `config:"cidr-list;;die-on-fail"`

	DebugMemoryProfilePath          string        `config:"file;;"`
	DebugCPUProfilePath             string        `config:"file;/tmp/felix-cpu-<timestamp>.pprof;"`
	DebugDisableLogDropping         bool          `config:"bool;false"`
	DebugSimulateCalcGraphHangAfter time.Duration `config:"seconds;0"`
	DebugSimulateDataplaneHangAfter time.Duration `config:"seconds;0"`

	// State tracking.

	// nameToSource tracks where we loaded each config param from.
	sourceToRawConfig map[Source]map[string]string
	rawValues         map[string]string
	Err               error

	IptablesNATOutgoingInterfaceFilter string `config:"iface-param;"`

	SidecarAccelerationEnabled bool `config:"bool;false"`
	XDPEnabled                 bool `config:"bool;false"`
	GenericXDPEnabled          bool `config:"bool;false"`
}

type ProtoPort struct {
	Protocol string
	Port     uint16
}

// Load parses and merges the rawData from one particular source into this config object.
// If there is a config value already loaded from a higher-priority source, then
// the new value will be ignored (after validation).
func (config *Config) UpdateFrom(rawData map[string]string, source Source) (changed bool, err error) {
	log.Infof("Merging in config from %v: %v", source, rawData)
	// Defensively take a copy of the raw data, in case we've been handed
	// a mutable map by mistake.
	rawDataCopy := make(map[string]string)
	for k, v := range rawData {
		if v == "" {
			log.WithFields(log.Fields{
				"name":   k,
				"source": source,
			}).Info("Ignoring empty configuration parameter. Use value 'none' if " +
				"your intention is to explicitly disable the default value.")
			continue
		}
		rawDataCopy[k] = v
	}
	config.sourceToRawConfig[source] = rawDataCopy

	changed, err = config.resolve()
	return
}

func (c *Config) InterfacePrefixes() []string {
	return strings.Split(c.InterfacePrefix, ",")
}

func (config *Config) OpenstackActive() bool {
	if strings.Contains(strings.ToLower(config.ClusterType), "openstack") {
		// OpenStack is explicitly known to be present.  Newer versions of the OpenStack plugin
		// set this flag.
		log.Debug("Cluster type contains OpenStack")
		return true
	}
	// If we get here, either OpenStack isn't present or we're running against an old version
	// of the OpenStack plugin, which doesn't set the flag.  Use heuristics based on the
	// presence of the OpenStack-related parameters.
	if config.MetadataAddr != "" && config.MetadataAddr != "127.0.0.1" {
		log.Debug("OpenStack metadata IP set to non-default, assuming OpenStack active")
		return true
	}
	if config.MetadataPort != 0 && config.MetadataPort != 8775 {
		log.Debug("OpenStack metadata port set to non-default, assuming OpenStack active")
		return true
	}
	for _, prefix := range config.InterfacePrefixes() {
		if prefix == "tap" {
			log.Debug("Interface prefix list contains 'tap', assuming OpenStack")
			return true
		}
	}
	log.Debug("No evidence this is an OpenStack deployment; disabling OpenStack special-cases")
	return false
}

func (config *Config) resolve() (changed bool, err error) {
	newRawValues := make(map[string]string)
	nameToSource := make(map[string]Source)
	for _, source := range SourcesInDescendingOrder {
	valueLoop:
		for rawName, rawValue := range config.sourceToRawConfig[source] {
			currentSource := nameToSource[rawName]
			param, ok := knownParams[strings.ToLower(rawName)]
			if !ok {
				if source >= currentSource {
					// Stash the raw value in case it's useful for
					// a plugin.  Since we don't know the canonical
					// name, use the raw name.
					newRawValues[rawName] = rawValue
					nameToSource[rawName] = source
				}
				log.WithField("raw name", rawName).Info(
					"Ignoring unknown config param.")
				continue valueLoop
			}
			metadata := param.GetMetadata()
			name := metadata.Name
			if metadata.Local && !source.Local() {
				log.Warningf("Ignoring local-only configuration for %v from %v",
					name, source)
				continue valueLoop
			}

			log.Infof("Parsing value for %v: %v (from %v)",
				name, rawValue, source)
			var value interface{}
			if strings.ToLower(rawValue) == "none" {
				// Special case: we allow a value of "none" to force the value to
				// the zero value for a field.  The zero value often differs from
				// the default value.  Typically, the zero value means "turn off
				// the feature".
				if metadata.NonZero {
					err = errors.New("Non-zero field cannot be set to none")
					log.Errorf(
						"Failed to parse value for %v: %v from source %v. %v",
						name, rawValue, source, err)
					config.Err = err
					return
				}
				value = metadata.ZeroValue
				log.Infof("Value set to 'none', replacing with zero-value: %#v.",
					value)
			} else {
				value, err = param.Parse(rawValue)
				if err != nil {
					logCxt := log.WithError(err).WithField("source", source)
					if metadata.DieOnParseFailure {
						logCxt.Error("Invalid (required) config value.")
						config.Err = err
						return
					} else {
						logCxt.WithField("default", metadata.Default).Warn(
							"Replacing invalid value with default")
						value = metadata.Default
						err = nil
					}
				}
			}

			log.Infof("Parsed value for %v: %v (from %v)",
				name, value, source)
			if source < currentSource {
				log.Infof("Skipping config value for %v from %v; "+
					"already have a value from %v", name,
					source, currentSource)
				continue
			}
			field := reflect.ValueOf(config).Elem().FieldByName(name)
			field.Set(reflect.ValueOf(value))
			newRawValues[name] = rawValue
			nameToSource[name] = source
		}
	}
	changed = !reflect.DeepEqual(newRawValues, config.rawValues)
	config.rawValues = newRawValues
	return
}

func (config *Config) setBy(name string, source Source) bool {
	_, set := config.sourceToRawConfig[source][name]
	return set
}

func (config *Config) setByConfigFileOrEnvironment(name string) bool {
	return config.setBy(name, ConfigFile) || config.setBy(name, EnvironmentVariable)
}

func (config *Config) DatastoreConfig() apiconfig.CalicoAPIConfig {
	// We want Felix's datastore connection to be fully configurable using the same
	// CALICO_XXX_YYY (or just XXX_YYY) environment variables that work for any libcalico-go
	// client - for both the etcdv3 and KDD cases.  However, for the etcd case, Felix has for a
	// long time supported FELIX_XXXYYY environment variables, and we want those to keep working
	// too.

	// To achieve that, first build a CalicoAPIConfig using libcalico-go's
	// LoadClientConfigFromEnvironment - which means incorporating defaults and CALICO_XXX_YYY
	// and XXX_YYY variables.
	cfg, err := apiconfig.LoadClientConfigFromEnvironment()
	if err != nil {
		log.WithError(err).Panic("Failed to create datastore config")
	}

	// Now allow FELIX_XXXYYY variables or XxxYyy config file settings to override that, in the
	// etcd case.
	if config.setByConfigFileOrEnvironment("DatastoreType") && config.DatastoreType == "etcdv3" {
		cfg.Spec.DatastoreType = apiconfig.EtcdV3
		// Endpoints.
		if config.setByConfigFileOrEnvironment("EtcdEndpoints") && len(config.EtcdEndpoints) > 0 {
			cfg.Spec.EtcdEndpoints = strings.Join(config.EtcdEndpoints, ",")
		} else if config.setByConfigFileOrEnvironment("EtcdAddr") {
			cfg.Spec.EtcdEndpoints = config.EtcdScheme + "://" + config.EtcdAddr
		}
		// TLS.
		if config.setByConfigFileOrEnvironment("EtcdKeyFile") {
			cfg.Spec.EtcdKeyFile = config.EtcdKeyFile
		}
		if config.setByConfigFileOrEnvironment("EtcdCertFile") {
			cfg.Spec.EtcdCertFile = config.EtcdCertFile
		}
		if config.setByConfigFileOrEnvironment("EtcdCaFile") {
			cfg.Spec.EtcdCACertFile = config.EtcdCaFile
		}
	}

	if !config.IpInIpEnabled && !config.VXLANEnabled {
		// Polling k8s for node updates is expensive (because we get many superfluous
		// updates) so disable if we don't need it.
		log.Info("Encap disabled, disabling node poll (if KDD is in use).")
		cfg.Spec.K8sDisableNodePoll = true
	}
	return *cfg
}

// Validate() performs cross-field validation.
func (config *Config) Validate() (err error) {
	if config.FelixHostname == "" {
		err = errors.New("Failed to determine hostname")
	}

	if config.DatastoreType == "etcdv3" && len(config.EtcdEndpoints) == 0 {
		if config.EtcdScheme == "" {
			err = errors.New("EtcdEndpoints and EtcdScheme both missing")
		}
		if config.EtcdAddr == "" {
			err = errors.New("EtcdEndpoints and EtcdAddr both missing")
		}
	}

	// If any client-side TLS config parameters are specified, they _all_ must be - except that
	// either TyphaCN or TyphaURISAN may be left unset.
	if config.TyphaCAFile != "" ||
		config.TyphaCertFile != "" ||
		config.TyphaKeyFile != "" ||
		config.TyphaCN != "" ||
		config.TyphaURISAN != "" {
		// Some TLS config specified.
		if config.TyphaKeyFile == "" ||
			config.TyphaCertFile == "" ||
			config.TyphaCAFile == "" ||
			(config.TyphaCN == "" && config.TyphaURISAN == "") {
			err = errors.New("If any Felix-Typha TLS config parameters are specified," +
				" they _all_ must be" +
				" - except that either TyphaCN or TyphaURISAN may be left unset.")
		}
	}

	if err != nil {
		config.Err = err
	}
	return
}

var knownParams map[string]param

func loadParams() {
	knownParams = make(map[string]param)
	config := Config{}
	kind := reflect.TypeOf(config)
	metaRegexp := regexp.MustCompile(`^([^;(]+)(?:\(([^)]*)\))?;` +
		`([^;]*)(?:;` +
		`([^;]*))?$`)
	for ii := 0; ii < kind.NumField(); ii++ {
		field := kind.Field(ii)
		tag := field.Tag.Get("config")
		if tag == "" {
			continue
		}
		captures := metaRegexp.FindStringSubmatch(tag)
		if len(captures) == 0 {
			log.Panicf("Failed to parse metadata for config param %v", field.Name)
		}
		log.Debugf("%v: metadata captures: %#v", field.Name, captures)
		kind := captures[1]       // Type: "int|oneof|bool|port-list|..."
		kindParams := captures[2] // Parameters for the type: e.g. for oneof "http,https"
		defaultStr := captures[3] // Default value e.g "1.0"
		flags := captures[4]
		var param param
		var err error
		switch kind {
		case "bool":
			param = &BoolParam{}
		case "int":
			min := minInt
			max := maxInt
			if kindParams != "" {
				minAndMax := strings.Split(kindParams, ",")
				min, err = strconv.Atoi(minAndMax[0])
				if err != nil {
					log.Panicf("Failed to parse min value for %v", field.Name)
				}
				max, err = strconv.Atoi(minAndMax[1])
				if err != nil {
					log.Panicf("Failed to parse max value for %v", field.Name)
				}
			}
			param = &IntParam{Min: min, Max: max}
		case "int32":
			param = &Int32Param{}
		case "mark-bitmask":
			param = &MarkBitmaskParam{}
		case "float":
			param = &FloatParam{}
		case "seconds":
			param = &SecondsParam{}
		case "millis":
			param = &MillisParam{}
		case "iface-list":
			param = &RegexpParam{Regexp: IfaceListRegexp,
				Msg: "invalid Linux interface name"}
		case "iface-list-regexp":
			param = &RegexpPatternListParam{
				NonRegexpElemRegexp: NonRegexpIfaceElemRegexp,
				RegexpElemRegexp:    RegexpIfaceElemRegexp,
				Delimiter:           ",",
				Msg:                 "list contains invalid Linux interface name or regex pattern",
			}
		case "iface-param":
			param = &RegexpParam{Regexp: IfaceParamRegexp,
				Msg: "invalid Linux interface parameter"}
		case "file":
			param = &FileParam{
				MustExist:  strings.Contains(kindParams, "must-exist"),
				Executable: strings.Contains(kindParams, "executable"),
			}
		case "authority":
			param = &RegexpParam{Regexp: AuthorityRegexp,
				Msg: "invalid URL authority"}
		case "ipv4":
			param = &Ipv4Param{}
		case "endpoint-list":
			param = &EndpointListParam{}
		case "port-list":
			param = &PortListParam{}
		case "portrange":
			param = &PortRangeParam{}
		case "portrange-list":
			param = &PortRangeListParam{}
		case "hostname":
			param = &RegexpParam{Regexp: HostnameRegexp,
				Msg: "invalid hostname"}
		case "host-address":
			param = &RegexpParam{Regexp: HostAddressRegexp,
				Msg: "invalid host address"}
		case "region":
			param = &RegionParam{}
		case "oneof":
			options := strings.Split(kindParams, ",")
			lowerCaseToCanon := make(map[string]string)
			for _, option := range options {
				lowerCaseToCanon[strings.ToLower(option)] = option
			}
			param = &OneofListParam{
				lowerCaseOptionsToCanonical: lowerCaseToCanon}
		case "string":
			param = &RegexpParam{Regexp: StringRegexp,
				Msg: "invalid string"}
		case "cidr-list":
			param = &CIDRListParam{}
		default:
			log.Panicf("Unknown type of parameter: %v", kind)
		}

		metadata := param.GetMetadata()
		metadata.Name = field.Name
		metadata.ZeroValue = reflect.ValueOf(config).FieldByName(field.Name).Interface()
		if strings.Index(flags, "non-zero") > -1 {
			metadata.NonZero = true
		}
		if strings.Index(flags, "die-on-fail") > -1 {
			metadata.DieOnParseFailure = true
		}
		if strings.Index(flags, "local") > -1 {
			metadata.Local = true
		}

		if defaultStr != "" {
			if strings.Index(flags, "skip-default-validation") > -1 {
				metadata.Default = defaultStr
			} else {
				// Parse the default value and save it in the metadata. Doing
				// that here ensures that we syntax-check the defaults now.
				defaultVal, err := param.Parse(defaultStr)
				if err != nil {
					log.Panicf("Invalid default value: %v", err)
				}
				metadata.Default = defaultVal
			}
		} else {
			metadata.Default = metadata.ZeroValue
		}
		knownParams[strings.ToLower(field.Name)] = param
	}
}

func (config *Config) RawValues() map[string]string {
	return config.rawValues
}

func New() *Config {
	if knownParams == nil {
		loadParams()
	}
	p := &Config{
		rawValues:         make(map[string]string),
		sourceToRawConfig: make(map[Source]map[string]string),
	}
	for _, param := range knownParams {
		param.setDefault(p)
	}
	hostname, err := names.Hostname()
	if err != nil {
		log.Warningf("Failed to get hostname from kernel, "+
			"trying HOSTNAME variable: %v", err)
		hostname = strings.ToLower(os.Getenv("HOSTNAME"))
	}
	p.FelixHostname = hostname
	return p
}

type param interface {
	GetMetadata() *Metadata
	Parse(raw string) (result interface{}, err error)
	setDefault(*Config)
}
