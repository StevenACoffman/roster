# JSON Schemas for OneRoster

This directory contains a [combined JSON Schema](./onerosterrosteringservicev1p2-getroster-200-responsepayload-schemav1p0.json) for an imagined roster retrieval endpoint.
This combines the JSON Schema documents of the OneRoster v1.2 specification REST bindings for the GetAll endpoints.

If you go to <https://www.imsglobal.org/spec/oneroster/latest/> it will redirect
you to the latest OneRoster version (v1p1 or v1p2, etc.) and you can substitute that version in the above URL
to find the version of ClassLink's API spec.
Using the IMS Global One Roster doc, find the Rostering Service REST Binding, and follow that so it should
tell you under Service Discovery:

```text
...hostname.../ims/oneroster/rostering/v1p2/discovery/onerosterv1p2rostersservice_openapi3_v1p0.json
```

You can download the IMS Global reference spec version:
Each [JSON schema](https://www.imsglobal.org/sites/default/files/spec/oneroster/v1p2/rostering-restbinding/OneRosterv1p2RosteringService_RESTBindv1p0.html#AppC) is available for each endpoint:

- [academicsessions](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallacademicsessions-200-responsepayload-schemav1p0.json)
- [classes](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallclasses-200-responsepayload-schemav1p0.json)
- [courses](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallcourses-200-responsepayload-schemav1p0.json)
- [demographics](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getalldemographics-200-responsepayload-schemav1p0.json)
- [enrollments](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallenrollments-200-responsepayload-schemav1p0.json)
- [gradingperiods](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallgradingperiods-200-responsepayload-schemav1p0.json)
- [orgs](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallorgs-200-responsepayload-schemav1p0.json)
- [schools](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallschools-200-responsepayload-schemav1p0.json)
- [students](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallstudents-200-responsepayload-schemav1p0.json)
- [teachers](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallteachers-200-responsepayload-schemav1p0.json)
- [terms](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallterms-200-responsepayload-schemav1p0.json)
- [users](https://purl.imsglobal.org/spec/or/v1p2/schema/jsd/onerosterrosteringservicev1p2-getallusers-200-responsepayload-schemav1p0.json)

## Understanding OneRoster and the Interoperability Challenge

OneRoster was developed by [1Edtech](https://ed.link/community/what-is-1edtech/), a non-profit focused on improving data interoperability in education. In this context, interoperability means different software systems can:

- Communicate with each other,
- Exchange data, and
- Reduce manual processes and errors.

Interoperability is critical for educators, as it provides a more complete view of student learning, enabling personalized learning.

In edtech, there are only two types of integrations: API integrations and specification-based integrations – like OneRoster and LTI ([more on LTI integrations here](https://ed.link/community/whats-the-difference-between-api-and-lti-integration/)). For SIS-specific integrations, edtech developers have a few methods to choose from:

- using an education specification, like **OneRoster**,
- building a custom integration to the **SIS provider’s API**, or
- connecting to an **aggregator** like Edlink

Essentially, OneRoster is one integration method that lays out how to implement the specification; it tells you how to “build the bridge” between one system to an SIS. From the view of edtech companies, this is where the interoperability problem begins.

To excel in learning institutions RFPs, and to deliver superior experiences, effective SIS interoperability is valuable. However, to become interoperable, edtech companies tend to continuously throw resources into their builds like:

- wasting months of product timeline,
- hiring an integration manager, and/or
- spending thousands of dollars in development time.

And if the integration does get built, the resource-wasting “interoperability game” never stops. Often, integrations are incomplete or limited in functionality, requiring further development. In other cases, institutions may sync all of their data, but the product might be unable to filter the fields it needs. Even simple troubleshooting can become too complex, necessitating the hiring of additional developers. There’s always another SIS to integrate with or another connection to manage.

In our experience, we’ve encountered challenges with SIS integrations, too:

- Some SIS providers only allow third-party vendors to use OneRoster integrations.
- Some SIS providers require you to pay for a test instance or limit access to their documentation.
- Some SIS providers lack sufficient technical support or are too slow to respond.
- Some SIS providers have significant functional limitations, like allowing read access but not write access.

Regardless of which challenge(s) edtech developers face within SIS interoperability, it is a fact that there will always be something.

1Edtech designed OneRoster to share class rosters, course materials, and grades. Even though SIS providers store many other types of data (such as facilities or period data), OneRoster isn’t designed to work with these fields. Developers must follow the specification closely to implement it correctly, which can be confusing.

To establish a OneRoster connection, developers might need to implement several different versions of the standard, like OneRoster 1.1, which supports OAuth 2.0, OAuth 1.0, or CSV file transfers. This is because the chosen implementation must match the SIS provider’s preferred method. Unlike API integrations, where the build is tailored to the product, OneRoster integrations aren’t customized.

Probably the largest (and most controversial) limitation of specifications like OneRoster is that they are reactive rather than proactive. OneRoster isn’t innovative; its focus isn’t to “find the best SIS integration method”. When a new version of a specification is released, the older version is often quickly deprecated, regardless of how many edtech companies or SIS providers are still using it.

On the other hand, developers can benefit from OneRoster. It’s widely recognized, and integrating with SIS providers can sometimes be more straightforward. OneRoster uses modern mechanics (OAuth, JSON) that are familiar, secure, and well-known. If a product needs an efficient way to access class rosters, course materials, and grade information, OneRoster could be a good solution.

### Changes Between OneRoster 1.1 and 1.2

These are the key differences we noticed (some points are taken directly from the documentation):

- Uses OAuth 2.0 Bearer Token exclusively
- Adds 3 new name properties from for users who wish to be called by a different name than their legal name; _preferredFirstName_, _preferredMiddleName_, and _preferredLastName_
- Supports users who have multiple roles within one or several organizations

The 'roles' class has been added to enable the assignment of any combination of role/org/account mappings for a user including date stamping to enable partial academic sessions.\
The _roles_ attribute has been added to the 'user' class.\
The _role_ attribute has been removed from the 'user' class.\
The _orgs_ attribute has been removed from the 'user' class.\
The _primaryOrg_ attribute added to the 'user' class.

- Updates the Gradebook Service to support Standards-Based Grading
- Adds the weighting scheme the organization or teacher designed to copy to the new system – supporting score scaling with grades
- Adds a new attribute to store non-numeric scores: _textScore_
- Adds a new endpoint for those with unique gradebook categories for each class: getCategoriesForClass
- Limits access to data within an endpoint through scoping down

These changes impact rostering…

- Reorganizes Rostering into its own Service Model with newly annotated endpoint: /ims/oneroster/rostering/v1p2
- Extends the vocabulary of the attribute _sex_ with the tokens 'other' and 'unspecified'
- Adds attribute _userMasterId_ to the “User” class to enable a definitive globally unique identifier (not the interoperability _sourcedId_ for the user)
- Adds attribute _roles_ to the “User” and “Roles” classes to enable the combination of role/org/account mappings for a user
- Adds the _resources_ attribute to “User” to identify resources available to the user
- Removes the _role_ and _orgs_ attributes from “User” class
- Adds _primaryOrg_ to the “User” class
- Adds _masterProfiles_ attribute to the “User”, “UserProfiles”, and “Credential” classes to enable assignment profiles to a user
- Makes the following extensible for easier profiling and internationalization: ClassTypeEnum, GenderEnum, OrgTypeEnum, RoleEnum' and 'SessionTypeEnum'
- Removes CEDS/SCEDS vocabulary in order to be replaced by more relevant (by geographic region) vocabulary
- Requres Service Provider to make a localized OpenAPI file available for Service Discovery

### To Recap

OneRoster 1.2 is the latest version of the specification. Key changes include:

- adding new attributes to expand the depth of information tied to a user,
- making rostering its own service, and
- moving to OAuth 2.0 for authentication.

OneRoster 1.2 is tied deeply to the interoperability problem. Learning institutions need interoperable edtech products but building this functionality is resource-heavy for companies. While OneRoster 1.2 can improve interoperability, integrating with it requires careful consideration of the product’s context, the development team’s capacity, available resources, the SIS provider’s requirements, and the needs of the product's end users.

______________________________________________________________________

## Read More on Data Standards

Here are other resources on Data Standards and Edlink to help you on your integration journey:

- [SIS Integration Challenges](https://ed.link/community/sis-integration-challenges/)
- [What is OneRoster?](https://ed.link/community/oneroster/)
- [What are the 9 Standards from 1EdTech Consortium?](https://ed.link/community/ims-global-standards/)
- [Introducing Edlink](https://ed.link/community/introducing-edlink/)
- [Our Mission at Edlink](https://ed.link/community/mission/)
- [What is the Edlink Unified API?](https://ed.link/community/what-is-the-edlink-unified-api/)
