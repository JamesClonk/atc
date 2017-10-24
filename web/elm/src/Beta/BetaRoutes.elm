module BetaRoutes exposing (ConcourseRoute, Route(..), customToString, navigateTo, parsePath, toString)

import Concourse.Pagination as Pagination
import Navigation exposing (Location)
import QueryString
import Route exposing (..)


type Route
    = Home
    | Beta
    | Build String String String String
    | Resource String String String
    | Job String String String
    | OneOffBuild String
    | BetaPipeline String String
    | SelectTeam
    | TeamLogin String
    | Dashboard


type alias ConcourseRoute =
    { logical : Route
    , queries : QueryString.QueryString
    , page : Maybe Pagination.Page
    , hash : String
    }



-- pages


build : Route.Route Route
build =
    Build := static "teams" </> string </> static "pipelines" </> string </> static "jobs" </> string </> static "builds" </> string


oneOffBuild : Route.Route Route
oneOffBuild =
    OneOffBuild := static "builds" </> string


resource : Route.Route Route
resource =
    Resource := static "teams" </> string </> static "pipelines" </> string </> static "resources" </> string


job : Route.Route Route
job =
    Job := static "teams" </> string </> static "pipelines" </> string </> static "jobs" </> string


login : Route.Route Route
login =
    SelectTeam := static "login"


betaPipeline : Route.Route Route
betaPipeline =
    BetaPipeline := static "beta" </> static "teams" </> string </> static "pipelines" </> string


teamLogin : Route.Route Route
teamLogin =
    TeamLogin := static "teams" </> string </> static "login"


dashboard : Route.Route Route
dashboard =
    Dashboard := static "beta" </> static "dashboard"


beta : Route.Route Route
beta =
    Beta := static "beta"



-- router


sitemap : Router Route
sitemap =
    router
        [ build
        , resource
        , job
        , login
        , oneOffBuild
        , betaPipeline
        , teamLogin
        , dashboard
        ]


match : String -> Route
match =
    Route.match sitemap
        >> Maybe.withDefault Home


toString : Route -> String
toString route =
    case route of
        Build teamName pipelineName jobName buildName ->
            reverse build [ teamName, pipelineName, jobName, buildName ]

        Job teamName pipelineName jobName ->
            reverse job [ teamName, pipelineName, jobName ]

        Resource teamName pipelineName resourceName ->
            reverse job [ teamName, pipelineName, resourceName ]

        OneOffBuild buildId ->
            reverse oneOffBuild [ buildId ]

        BetaPipeline teamName pipelineName ->
            reverse betaPipeline [ teamName, pipelineName ]

        SelectTeam ->
            reverse login []

        TeamLogin teamName ->
            reverse teamLogin [ teamName ]

        Dashboard ->
            reverse dashboard []

        Beta ->
            "/beta"

        Home ->
            "/"


parsePath : Location -> ConcourseRoute
parsePath location =
    { logical = match <| location.pathname
    , queries = QueryString.parse location.search |> QueryString.remove "csrf_token"
    , page = createPageFromSearch location.search
    , hash = location.hash
    }


customToString : ConcourseRoute -> String
customToString route =
    toString route.logical ++ QueryString.render route.queries


createPageFromSearch : String -> Maybe Pagination.Page
createPageFromSearch search =
    let
        q =
            QueryString.parse search

        until =
            QueryString.one QueryString.int "until" q

        since =
            QueryString.one QueryString.int "since" q

        limit =
            Maybe.withDefault 100 <| QueryString.one QueryString.int "limit" q
    in
    case ( since, until ) of
        ( Nothing, Just u ) ->
            Just
                { direction = Pagination.Until u
                , limit = limit
                }

        ( Just s, Nothing ) ->
            Just
                { direction = Pagination.Since s
                , limit = limit
                }

        _ ->
            Nothing


navigateTo : Route -> Cmd msg
navigateTo =
    toString >> Navigation.newUrl
