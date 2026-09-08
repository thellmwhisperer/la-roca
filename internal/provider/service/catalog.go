package service

import (
	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/provider/layers"
	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"sync"
)

var theModelsSchema = sync.OnceValue(func() query.Schema {
	return query.ReadSchema(data.Schema+"\n"+data.SearchSchema, sqlgate.HiddenTables())
})

func (s *Service) LayerRegistry() layers.Registry { return s.registry }
func (s *Service) Options() Options               { return s.opts }
