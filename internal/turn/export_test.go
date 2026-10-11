package turn

// BeginTool and EndTool expose the turn side of Control to tests of its
// interleaving with the actor side.
func BeginTool(c *Control) bool { return c.beginTool() }

func EndTool(c *Control) bool { return c.endTool() }
