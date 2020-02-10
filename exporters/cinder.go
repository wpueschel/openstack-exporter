package exporters

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/services"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/volumetenants"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/prometheus/client_golang/prometheus"
)

type CinderExporter struct {
	BaseOpenStackExporter
}

var volume_status = []string{
	"creating",
	"available",
	"reserved",
	"attaching",
	"detaching",
	"in-use",
	"maintenance",
	"deleting",
	"awaiting-transfer",
	"error",
	"error_deleting",
	"backing-up",
	"restoring-backup",
	"error_backing-up",
	"error_restoring",
	"error_extending",
	"downloading",
	"uploading",
	"retyping",
	"extending",
}

func mapVolumeStatus(volStatus string) int {
	for idx, status := range volume_status {
		if status == strings.ToLower(volStatus) {
			return idx
		}
	}
	return -1
}

var defaultCinderMetrics = []Metric{
	{Name: "volumes", Fn: ListVolumes},
	{Name: "snapshots", Fn: ListSnapshots},
	{Name: "agent_state", Labels: []string{"hostname", "service", "adminState", "zone"}, Fn: ListCinderAgentState},
	{Name: "volume_status", Labels: []string{"id", "name", "status", "bootable", "tenant_id", "size", "volume_type"}, Fn: nil},
	{Name: "limits_max", Labels: []string{"tenant"}, Fn: ListCinderLimits},
	{Name: "limits_used", Labels: []string{"tenant"}},
}

func NewCinderExporter(client *gophercloud.ServiceClient, prefix string, disabledMetrics []string) (*CinderExporter, error) {
	exporter := CinderExporter{
		BaseOpenStackExporter{
			Name:            "cinder",
			Prefix:          prefix,
			Client:          client,
			DisabledMetrics: disabledMetrics,
		},
	}
	for _, metric := range defaultCinderMetrics {
		exporter.AddMetric(metric.Name, metric.Fn, metric.Labels, nil)
	}

	return &exporter, nil
}

func ListVolumes(exporter *BaseOpenStackExporter, ch chan<- prometheus.Metric) error {
	type VolumeWithExt struct {
		volumes.Volume
		volumetenants.VolumeTenantExt
	}

	var allVolumes []VolumeWithExt

	allPagesVolumes, err := volumes.List(exporter.Client, volumes.ListOpts{
		AllTenants: true,
	}).AllPages()
	if err != nil {
		return err
	}

	err = volumes.ExtractVolumesInto(allPagesVolumes, &allVolumes)
	if err != nil {
		return err
	}

	ch <- prometheus.MustNewConstMetric(exporter.Metrics["volumes"].Metric,
		prometheus.GaugeValue, float64(len(allVolumes)))

	// Volume status metrics
	for _, volume := range allVolumes {
		ch <- prometheus.MustNewConstMetric(exporter.Metrics["volume_status"].Metric,
			prometheus.GaugeValue, float64(mapVolumeStatus(volume.Status)), volume.ID, volume.Name,
			volume.Status, volume.Bootable, volume.TenantID, strconv.Itoa(volume.Size), volume.VolumeType)
	}

	return nil
}

func ListSnapshots(exporter *BaseOpenStackExporter, ch chan<- prometheus.Metric) error {
	var allSnapshots []snapshots.Snapshot

	allPagesSnapshot, err := snapshots.List(exporter.Client, snapshots.ListOpts{AllTenants: true}).AllPages()
	if err != nil {
		return err
	}

	allSnapshots, err = snapshots.ExtractSnapshots(allPagesSnapshot)
	if err != nil {
		return err
	}

	ch <- prometheus.MustNewConstMetric(exporter.Metrics["snapshots"].Metric,
		prometheus.GaugeValue, float64(len(allSnapshots)))

	return nil
}

func ListCinderAgentState(exporter *BaseOpenStackExporter, ch chan<- prometheus.Metric) error {
	var allServices []services.Service

	allPagesService, err := services.List(exporter.Client, services.ListOpts{}).AllPages()
	if err != nil {
		return err
	}
	allServices, err = services.ExtractServices(allPagesService)
	if err != nil {
		return err
	}

	for _, service := range allServices {
		var state int = 0
		if service.State == "up" {
			state = 1
		}
		ch <- prometheus.MustNewConstMetric(exporter.Metrics["agent_state"].Metric,
			prometheus.CounterValue, float64(state), service.Host, service.Binary, service.Status, service.Zone)
	}

	return nil
}

func ListCinderLimits(exporter *BaseOpenStackExporter, ch chan<- prometheus.Metric) error {

	var allProjects []projects.Project
	var eo gophercloud.EndpointOpts

	// We need a list of all tenants/projects.
	// Therefore, within this nova exporter we need
	// to create an openstack client for the Identity/Keystone API.
	// If possible, use the EndpointOpts spefic to the identity service.
	if v, ok := endpointOpts["identity"]; ok {
		eo = v
	} else if v, ok := endpointOpts["cinder"]; ok {
		eo = v
	} else {
		return errors.New("No EndpointOpts available to create Identity client")
	}

	c, err := openstack.NewIdentityV3(exporter.Client.ProviderClient, eo)
	if err != nil {
		return err
	}

	allPagesProject, err := projects.List(c, projects.ListOpts{}).AllPages()
	if err != nil {
		return err
	}

	allProjects, err = projects.ExtractProjects(allPagesProject)
	if err != nil {
		return err
	}

	for _, p := range allProjects {

		limits, err := quotasets.Get(exporter.Client, p.ID).Extract()
		if err != nil {
			return err
		}

		limitsUsed, err := quotasets.GetUsage(exporter.Client, p.ID).Extract()
		if err != nil {
			return err
		}

		ch <- prometheus.MustNewConstMetric(exporter.Metrics["limits_max"].Metric,
			prometheus.GaugeValue, float64(limits.Gigabytes), p.Name)

		ch <- prometheus.MustNewConstMetric(exporter.Metrics["limits_used"].Metric,
			prometheus.GaugeValue, float64(limitsUsed.Gigabytes.InUse), p.Name)

	}

	return nil

}
