load("//rules/typescript:index.bzl", "ts_library")

package(default_visibility = ["//enterprise:__subpackages__"])

exports_files(["trends.css"])

ts_library(
    name = "trends",
    srcs = glob(["*.tsx"]),
    deps = [
        "//app/auth",
        "//app/capabilities",
        "//app/components/button",
        "//app/format",
        "//app/router",
        "//app/invocation",
        "//app/util:proto",
        "//enterprise/app/filter",
        "//proto:invocation_ts_proto",
        "@npm//@types/react",
        "@npm//moment",
        "@npm//react",
        "@npm//recharts",
        "@npm//rxjs",
    ],
)
