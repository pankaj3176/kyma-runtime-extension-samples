package main

import (
	"log"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/SAP-samples/kyma-runtime-extension-samples/saas-provisioning/internal/api"
)

func main() {

	router := mux.NewRouter().StrictSlash(true)

	router.HandleFunc("/callback/v1.0/tenants/{tenant}", api.Provision).Methods("PUT")
	router.HandleFunc("/callback/v1.0/tenants/{tenant}", api.Deprovision).Methods("DELETE")

	log.Fatal(http.ListenAndServe(":8000", router))
}
