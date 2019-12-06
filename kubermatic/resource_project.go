package kubermatic

import (
	"fmt"
	"net"
	"net/http"

	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/kubermatic/go-kubermatic/client/project"
	"github.com/kubermatic/go-kubermatic/models"
)

const (
	projectActive   = "Active"
	projectInactive = "Inactive"
)

func resourceProject() *schema.Resource {
	return &schema.Resource{
		Create: resourceProjectCreate,
		Read:   resourceProjectRead,
		Update: resourceProjectUpdate,
		Delete: resourceProjectDelete,

		Schema: map[string]*schema.Schema{
			"name": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
			},
			"labels": &schema.Schema{
				Type:     schema.TypeMap,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"status": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},
			"creation_timestamp": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},
			"deletion_timestamp": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceProjectCreate(d *schema.ResourceData, m interface{}) error {
	k := m.(*kubermaticProvider)
	p := project.NewCreateProjectParams()

	p.Body.Name = d.Get("name").(string)
	if l, ok := d.GetOk("labels"); ok {
		p.Body.Labels = make(map[string]string)
		att := l.(map[string]interface{})
		for key, val := range att {
			p.Body.Labels[key] = val.(string)
		}
	}

	r, err := k.client.Project.CreateProject(p, k.auth)
	if err != nil {
		return fmt.Errorf("error when creating a project: %s", err)
	}
	id := r.Payload.ID

	createStateConf := &resource.StateChangeConf{
		Pending: []string{
			projectInactive,
		},
		Target: []string{
			projectActive,
		},
		Refresh: func() (interface{}, string, error) {
			p := project.NewGetProjectParams()
			r, err := k.client.Project.GetProject(p.WithProjectID(id), k.auth)
			if err != nil {
				return nil, "", err
			}
			k.log.Debugf("creating project '%s', currently in '%s' state", r.Payload.ID, r.Payload.Status)
			return r, r.Payload.Status, nil
		},
		Timeout:    d.Timeout(schema.TimeoutCreate),
		MinTimeout: retryTimeout,
		Delay:      requestDelay,
	}
	_, err = createStateConf.WaitForState()
	if err != nil {
		k.log.Debugf("error while waiting for project '%s' to be created: %s", id, err)
		return fmt.Errorf("error while waiting for project '%s' to be created: %s", id, err)
	}

	d.SetId(id)
	return resourceProjectRead(d, m)
}

func resourceProjectRead(d *schema.ResourceData, m interface{}) error {
	k := m.(*kubermaticProvider)
	p := project.NewGetProjectParams()

	return resource.Retry(d.Timeout(schema.TimeoutRead), func() *resource.RetryError {
		r, err := k.client.Project.GetProject(p.WithProjectID(d.Id()), k.auth)
		if err != nil {
			switch e := err.(type) {
			case net.Error:
				if e.Timeout() || e.Temporary() {
					return resource.RetryableError(
						fmt.Errorf("network issue occured while trying to read project '%s': %s", d.Id(), e.Error()),
					)
				}
				return resource.NonRetryableError(e)
			case *project.GetProjectConflict, *project.GetProjectUnauthorized:
				return resource.NonRetryableError(e)
			case *project.GetProjectDefault:
				if e.Code() == http.StatusForbidden || e.Code() == http.StatusNotFound {
					// remove a project from terraform state file that a user does not have access
					k.log.Debugf("removing project '%s' from terraform state file, code '%d' has been returned", d.Id(), e.Code())
					d.SetId("")
					return nil
				}
			}

			k.log.Debugf("unexpected error for project '%s': %v", d.Id(), err)
			return resource.NonRetryableError(err)
		}

		err = d.Set("name", r.Payload.Name)
		if err != nil {
			return resource.NonRetryableError(err)
		}

		err = d.Set("labels", r.Payload.Labels)
		if err != nil {
			return resource.NonRetryableError(err)
		}

		err = d.Set("status", r.Payload.Status)
		if err != nil {
			return resource.NonRetryableError(err)
		}

		err = d.Set("creation_timestamp", r.Payload.CreationTimestamp.String())
		if err != nil {
			return resource.NonRetryableError(err)
		}

		err = d.Set("deletion_timestamp", r.Payload.DeletionTimestamp.String())
		if err != nil {
			return resource.NonRetryableError(err)
		}

		return nil
	})
}

func resourceProjectUpdate(d *schema.ResourceData, m interface{}) error {
	k := m.(*kubermaticProvider)
	p := project.NewUpdateProjectParams()
	p.Body = &models.Project{
		// name is always required for update requests, otherwise bad request returns
		Name: d.Get("name").(string),
	}

	if d.HasChange("name") {
		old, updt := d.GetChange("name")
		k.log.Debugf("project name '%s' change discovered from '%s' to '%s'", d.Id(), old.(string), updt.(string))
		p.Body.Name = d.Get("name").(string)
	}
	if d.HasChange("labels") {
		p.Body.Labels = make(map[string]string)
		for key, val := range d.Get("labels").(map[string]interface{}) {
			p.Body.Labels[key] = val.(string)
		}
		old, updt := d.GetChange("labels")
		k.log.Debugf("change discovered for project '%s': '%+v' changed to '%+v'",
			d.Id(), old.(map[string]interface{}), updt.(map[string]interface{}))
	}

	_, err := k.client.Project.UpdateProject(p.WithProjectID(d.Id()), k.auth)
	if err != nil {
		return fmt.Errorf("unable to update project '%s': %v", d.Id(), err)
	}

	return resourceProjectRead(d, m)
}

func resourceProjectDelete(d *schema.ResourceData, m interface{}) error {
	k := m.(*kubermaticProvider)
	p := project.NewDeleteProjectParams()
	_, err := k.client.Project.DeleteProject(p.WithProjectID(d.Id()), k.auth)
	if err != nil {
		return fmt.Errorf("unable to delete project '%s': %s", d.Id(), err)
	}

	err = resource.Retry(d.Timeout(schema.TimeoutDelete), func() *resource.RetryError {
		p := project.NewGetProjectParams()
		r, err := k.client.Project.GetProject(p.WithProjectID(d.Id()), k.auth)
		if err != nil {
			e, ok := err.(*project.GetProjectDefault)
			if ok && (e.Code() == http.StatusForbidden || e.Code() == http.StatusNotFound) {
				k.log.Debugf("project '%s' has been destroyed, returned http code: %d", d.Id(), e.Code())
				return nil
			}
			return resource.NonRetryableError(err)
		}
		k.log.Debugf("project '%s' deletion in progress, deletionTimestamp: %s, status: %s",
			d.Id(), r.Payload.DeletionTimestamp.String(), r.Payload.Status)
		return resource.RetryableError(
			fmt.Errorf("project '%s' still exists, currently in '%s' state", d.Id(), r.Payload.Status),
		)
	})
	if err != nil {
		return err
	}

	return nil
}