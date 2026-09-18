// Package record defines the in-memory batch contract shared by Fetchers, the Writer and Go
// transforms. A Batch pairs a small typed Column schema with rows of loosely typed values, so
// every stage of the pipeline can validate a batch before it is written or transformed.
//
// Data-engineering concept: the schema-carrying batch that forms the extract/load contract.
package record
