"""The project's message-template formatting, without a runnable framework."""

from dataclasses import dataclass, field
from string import Formatter

from .messages import BaseMessage, as_message, to_messages


@dataclass
class ChatPromptValue:
    messages: list

    def to_messages(self):
        return to_messages(self.messages)


@dataclass(frozen=True)
class MessagesPlaceholder:
    variable_name: str


@dataclass
class ChatPromptTemplate:
    messages: list
    partial_variables: dict = field(default_factory=dict)

    @classmethod
    def from_messages(cls, messages):
        return cls(list(messages))

    def partial(self, **kwargs):
        return type(self)(self.messages, {**self.partial_variables, **kwargs})

    def format_messages(self, **kwargs):
        variables = {**self.partial_variables, **kwargs}
        result = []
        for item in self.messages:
            if isinstance(item, MessagesPlaceholder):
                result.extend(to_messages(variables[item.variable_name]))
            elif isinstance(item, BaseMessage):
                result.append(item)
            else:
                role, template = item
                result.append(as_message((role, template.format(**variables))))
        return result

    def invoke(self, input):
        if not isinstance(input, dict):
            names = set()
            for item in self.messages:
                if isinstance(item, MessagesPlaceholder):
                    names.add(item.variable_name)
                elif not isinstance(item, BaseMessage):
                    names.update(name for _, name, _, _ in Formatter().parse(item[1]) if name)
            names -= self.partial_variables.keys()
            if len(names) != 1:
                raise TypeError("A template with multiple variables requires a dict input")
            input = {next(iter(names)): input}
        return ChatPromptValue(self.format_messages(**input))

    def __or__(self, model):
        return PromptCall(self, model)


@dataclass
class PromptCall:
    prompt: ChatPromptTemplate
    model: object

    def invoke(self, input, config=None, **kwargs):
        return self.model.invoke(self.prompt.invoke(input), config=config, **kwargs)
