package csom

import (
	"fmt"

	"github.com/koltyakov/gosip/csom"
)

// BuildOneDriveQuery builds the CSOM XML payload for fetching OneDrive personal sites.
func BuildOneDriveQuery(filter, startIndex string) (string, error) {
	// Constants for OneDrive personal site enumeration
	const (
		includePersonalSites = "1"       // Always include personal sites
		webTemplate          = "SPSPERS" // OneDrive personal site template
	)

	builder := csom.NewBuilder()
	constructorXml := `<Constructor Id="{{.ID}}" TypeId="{268004ae-ef6b-4e9b-8425-127220d84719}" />`
	tenantObject, _ := builder.AddObject(csom.NewObject(constructorXml), nil)

	methodParams := []string{
		fmt.Sprintf(`
			<Parameter TypeId="{b92aeee2-c92c-4b67-abcc-024e471bc140}">
				<Property Name="Filter" Type="String">%s</Property>
				<Property Name="IncludeDetail" Type="Boolean">false</Property>
				<Property Name="IncludePersonalSite" Type="Enum">%s</Property>
				<Property Name="StartIndex" Type="String">%s</Property>
				<Property Name="Template" Type="String">%s</Property>
			</Parameter>`,
			filter, includePersonalSites, startIndex, webTemplate),
	}

	methodObject, _ := builder.AddObject(csom.NewObjectMethod("GetSitePropertiesFromSharePointByFilters", methodParams), tenantObject)
	builder.AddAction(csom.NewAction(`<ObjectPath Id="{{.ID}}" ObjectPathId="{{.ObjectID}}" />`), tenantObject)
	builder.AddAction(csom.NewAction(`<ObjectPath Id="{{.ID}}" ObjectPathId="{{.ObjectID}}" />`), methodObject)

	queryXml := `<Query Id="{{.ID}}" ObjectPathId="{{.ObjectID}}"><Query SelectAllProperties="true"><Properties /></Query><ChildItemQuery SelectAllProperties="true"><Properties /></ChildItemQuery></Query>`
	builder.AddAction(csom.NewAction(queryXml), methodObject)

	return builder.Compile()
}
