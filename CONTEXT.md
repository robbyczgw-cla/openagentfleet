# OpenAgentFleet workspace

OpenAgentFleet keeps Agents, their tasks, and scheduled work in one local workspace.

## Language

**Agent**:
A persistent teammate with a name, role, and configured tools. An Agent can handle many tasks over time.
_Avoid_: Bot in new product copy; existing `bot_id` fields remain compatible.

**Task**:
One Agent run presented with its original brief, status, result, and files. A conversation can contain several tasks.
_Avoid_: Conversation when referring to one run.

**Result**:
The final answer saved for one task. Later messages in the conversation do not replace it.
_Avoid_: Latest reply when identifying a task's answer.

**Artifact**:
A saved copy of a file linked from a task's result. The copy belongs to that task even if the Agent later changes the source file.
_Avoid_: Attachment, which denotes a file supplied as input.

**Routine**:
An Agent-owned instruction with a schedule and an enabled, paused, or disabled state. Each occurrence can start a task.
_Avoid_: Workflow template when referring to a scheduled instance.

**Workflow template**:
A portable set of instructions and schedule defaults used to create a disabled routine. It has no assigned Agent or app access until the owner configures them.
_Avoid_: Routine export when implying that credentials or execution history travel with it.

**Connected app**:
An external service the owner has made available to selected Agents. The GitHub connection grants read access to selected repositories.
_Avoid_: Login alone when referring to an Agent's permission to use a service.
